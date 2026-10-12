package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
)

func (e *testEnv) deviceLogin(username, password, ip string) (*DeviceLoginResult, error) {
	return e.svc.DeviceLogin(e.ctx, DeviceLoginInput{Username: username, Password: password, DeviceName: "Work laptop", UserAgent: "tray", IP: ip})
}

func (e *testEnv) mustDeviceLogin(username string) *DeviceLoginResult {
	e.t.Helper()
	res, err := e.deviceLogin(username, goodPassword, "192.168.1.10")
	if err != nil {
		e.t.Fatalf("DeviceLogin: %v", err)
	}
	return res
}

func (e *testEnv) deviceAuth(token string) *Principal {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	p, err := e.svc.DeviceAuthenticator().Authenticate(r)
	if err != nil {
		e.t.Fatalf("Authenticate: %v", err)
	}
	return p
}

func TestDeviceLoginIssuesHashedScopedToken(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustDeviceLogin("alice")
	if len(res.Token) < 20 || res.Token[:4] != "bub_" || res.HandoffCode == "" || res.DeviceID == "" {
		t.Fatalf("result = %+v", res)
	}
	if n := e.count(`SELECT COUNT(*) FROM device_tokens WHERE token_hash = ? AND prefix = ?`, HashToken(res.Token), res.Token[:8]); n != 1 {
		t.Fatalf("hashed device token rows = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM device_tokens WHERE token_hash = ?`, []byte(res.Token)); n != 0 {
		t.Fatal("the token itself is stored")
	}
	p := e.deviceAuth(res.Token)
	if p == nil || p.Via != ViaDevice || p.Device == nil || p.User.Username != "alice" || p.Token != nil || p.Session != nil {
		t.Fatalf("principal = %+v", p)
	}
	want := []string{"events:read", "messages:read", "notify"}
	if len(p.Device.Scopes) != 3 || p.Device.Scopes[0] != want[0] || p.Device.Scopes[1] != want[1] || p.Device.Scopes[2] != want[2] {
		t.Fatalf("scopes = %v", p.Device.Scopes)
	}
	if e.auditCount(audit.ActionDeviceLogin) != 1 {
		t.Fatal("device login not audited")
	}
	if e.deviceAuth(res.Token+"x") != nil || e.deviceAuth("bub_unknown") != nil {
		t.Fatal("unknown tokens authenticate")
	}
}

func TestDeviceLoginSharesLockoutWithLogin(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	for range 5 {
		if _, err := e.deviceLogin("alice", "wrong password!", "192.168.1.10"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	}
	var rl *RateLimitedError
	if _, err := e.login("alice", goodPassword); !errors.As(err, &rl) {
		t.Fatalf("login after device failures: %v", err)
	}
	if _, err := e.deviceLogin("alice", goodPassword, "192.168.1.11"); !errors.As(err, &rl) {
		t.Fatalf("device login after failures: %v", err)
	}
	if e.auditCount(audit.ActionLoginFailure) != 5 {
		t.Fatalf("failures audited = %d", e.auditCount(audit.ActionLoginFailure))
	}
	e.clock.Advance(16 * time.Minute)
	if _, err := e.deviceLogin("alice", goodPassword, "192.168.1.11"); err != nil {
		t.Fatalf("after the lockout: %v", err)
	}
}

func TestDeviceLoginFailuresAreGeneric(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	for _, c := range []struct{ user, pw string }{{"alice", "nope nope nope"}, {"nobody", goodPassword}} {
		if _, err := e.deviceLogin(c.user, c.pw, "192.168.1.10"); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: err = %v", c.user, err)
		}
	}
	var ve *ValidationError
	if _, err := e.svc.DeviceLogin(e.ctx, DeviceLoginInput{Username: "alice", Password: goodPassword, DeviceName: " \n", IP: "192.168.1.10"}); !errors.As(err, &ve) {
		t.Errorf("bad device name: %v", err)
	}
}

func TestHandoffCodeIsSingleUse(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustDeviceLogin("alice")
	sess, err := e.svc.RedeemHandoff(e.ctx, res.HandoffCode, "webview", "192.168.1.10", "")
	if err != nil {
		t.Fatalf("RedeemHandoff: %v", err)
	}
	if sess.User.Username != "alice" || sess.Session.Kind != SessionHandoff || sess.Token == "" {
		t.Fatalf("session = %+v", sess)
	}
	if _, err := e.svc.Authenticate(e.ctx, sess.Token); err != nil {
		t.Fatalf("the handoff session does not authenticate: %v", err)
	}
	if _, err := e.svc.RedeemHandoff(e.ctx, res.HandoffCode, "webview", "192.168.1.10", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second redeem: %v", err)
	}
	if e.auditCount(audit.ActionHandoff) != 1 {
		t.Fatal("handoff not audited")
	}
}

func TestHandoffCodeExpiresAfter60Seconds(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustDeviceLogin("alice")
	e.clock.Advance(HandoffTTL)
	if _, err := e.svc.RedeemHandoff(e.ctx, res.HandoffCode, "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}
	// A fresh code from the device works until 1 s before the deadline.
	p := e.deviceAuth(res.Token)
	code, err := e.svc.IssueHandoff(p)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(HandoffTTL - time.Second)
	if _, err := e.svc.RedeemHandoff(e.ctx, code, "", "", ""); err != nil {
		t.Fatalf("code at 59 s: %v", err)
	}
	if _, err := e.svc.RedeemHandoff(e.ctx, "", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty code: %v", err)
	}
	if _, err := e.svc.RedeemHandoff(e.ctx, "not-a-code", "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown code: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM sessions WHERE kind = 'handoff'`) != 1 {
		t.Fatal("rejected codes created sessions")
	}
}

func TestHandoffCodeDiesWithItsDevice(t *testing.T) {
	e := newEnv(t)
	u := e.addUser("alice", RoleMember)
	res := e.mustDeviceLogin("alice")
	if err := e.svc.RevokeDevice(e.ctx, audit.User(u.ID, ""), u.ID, res.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RedeemHandoff(e.ctx, res.HandoffCode, "", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code of a revoked device: %v", err)
	}
}

func TestHandoffRotatesPresentedSession(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	old := e.mustLogin("alice")
	res := e.mustDeviceLogin("alice")
	if _, err := e.svc.RedeemHandoff(e.ctx, res.HandoffCode, "", "", old.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, old.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old session survived: %v", err)
	}
}

func TestRevokeDeviceAndListing(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	bob := e.addUser("bob", RoleMember)
	d1 := e.mustDeviceLogin("alice")
	e.clock.Advance(time.Second)
	d2 := e.mustDeviceLogin("alice")
	list, err := e.svc.ListDevices(e.ctx, alice.ID)
	if err != nil || len(list) != 2 || list[0].ID != d2.DeviceID || list[0].Name != "Work laptop" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := e.svc.RevokeDevice(e.ctx, audit.User(bob.ID, ""), bob.ID, d1.DeviceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking a device of another user: %v", err)
	}
	before := e.eventCount("session.revoked")
	if err := e.svc.RevokeDevice(e.ctx, audit.User(alice.ID, ""), alice.ID, d1.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeDevice(e.ctx, audit.User(alice.ID, ""), alice.ID, d1.DeviceID); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if e.eventCount("session.revoked") != before+1 || e.auditCount(audit.ActionDeviceRevoke) != 1 {
		t.Fatal("revoke must close sockets and audit exactly once")
	}
	if e.deviceAuth(d1.Token) != nil || e.deviceAuth(d2.Token) == nil {
		t.Fatal("revocation hit the wrong device")
	}
	if list, _ := e.svc.ListDevices(e.ctx, alice.ID); len(list) != 1 {
		t.Fatalf("list after revoke = %d", len(list))
	}
}

func TestDevicesRevokedOnPasswordChangeAndDeactivation(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("boss", RoleAdmin)
	alice := e.addUser("alice", RoleMember)
	carol := e.addUser("carol", RoleMember)

	d := e.mustDeviceLogin("alice")
	if err := e.svc.ChangePassword(e.ctx, ChangePasswordInput{UserID: alice.ID, Current: goodPassword, New: "a brand new passphrase", IP: "192.168.1.10"}); err != nil {
		t.Fatal(err)
	}
	if e.deviceAuth(d.Token) != nil {
		t.Fatal("device token survived a password change")
	}

	c := e.mustDeviceLogin("carol")
	deact := true
	if _, err := e.svc.AdminUpdateUser(e.ctx, audit.User(admin.ID, ""), carol.ID, AdminUserPatch{Deactivated: &deact}); err != nil {
		t.Fatal(err)
	}
	if e.deviceAuth(c.Token) != nil {
		t.Fatal("device token survived deactivation")
	}
	if _, err := e.deviceLogin("carol", goodPassword, "192.168.1.10"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("deactivated user device login: %v", err)
	}
}

func TestDeviceLimit(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	for range MaxDevices {
		e.mustDeviceLogin("alice")
	}
	var ve *ValidationError
	if _, err := e.deviceLogin("alice", goodPassword, "192.168.1.10"); !errors.As(err, &ve) {
		t.Fatalf("over the limit: %v", err)
	}
}
