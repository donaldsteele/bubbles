package auth

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
)

func TestSessionStoresOnlyHash(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")

	if len(res.Token) != 52 || res.Token != strings.ToLower(res.Token) {
		t.Errorf("token = %q, want 52 lower-case base32 characters", res.Token)
	}
	var stored []byte
	if err := e.db.Read().QueryRow(`SELECT id_hash FROM sessions WHERE user_id = ?`, res.User.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, HashToken(res.Token)) || len(stored) != 32 {
		t.Errorf("stored id_hash = %x, want SHA-256 of the token", stored)
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions WHERE CAST(id_hash AS TEXT) = ? OR user_agent = ? OR ip = ?`, res.Token, res.Token, res.Token); n != 0 {
		t.Error("raw token found in the sessions table")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ? OR target_id = ?`, "%"+res.Token+"%", res.Token); n != 0 {
		t.Error("raw token found in the audit log")
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE payload LIKE ?`, "%"+res.Token+"%"); n != 0 {
		t.Error("raw token found in events")
	}
	if res.Session.ID != PublicSessionID(stored) || len(res.Session.ID) != 32 {
		t.Errorf("public id = %q", res.Session.ID)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	now := newFakeClock().Now()
	c := NewCookie("tok", now.Add(AbsoluteLifetime), now)
	if c.Name != "__Host-bubbles_session" || c.Value != "tok" || c.Path != "/" || c.Domain != "" ||
		!c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.MaxAge != 90*24*3600 {
		t.Errorf("cookie = %+v", c)
	}
	want := "__Host-bubbles_session=tok; Path=/; Max-Age=7776000; HttpOnly; Secure; SameSite=Strict"
	if got := c.String(); got != want {
		t.Errorf("Set-Cookie = %q, want %q", got, want)
	}
	clear := ClearCookie()
	if clear.MaxAge >= 0 || clear.Name != CookieName || !clear.Secure || !clear.HttpOnly || clear.SameSite != http.SameSiteStrictMode {
		t.Errorf("clear cookie = %+v", clear)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	day := 24 * time.Hour

	if got, want := res.Session.IdleExpiresAt-res.Session.CreatedAt, (30 * day).Milliseconds(); got != want {
		t.Errorf("idle window = %dms, want 30 days", got)
	}
	if got, want := res.Session.AbsExpiresAt-res.Session.CreatedAt, (90 * day).Milliseconds(); got != want {
		t.Errorf("absolute window = %dms, want 90 days", got)
	}

	// Activity within the idle window keeps the session alive; each touch
	// slides the idle expiry, but never past the absolute expiry.
	for i := 0; i < 3; i++ {
		e.clock.Advance(29 * day)
		if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
			t.Fatalf("day %d: %v", (i+1)*29, err)
		}
	}
	// Now at day 87: the idle expiry was clamped to the absolute one (day 90).
	e.clock.Advance(2*day + 23*time.Hour)
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Fatalf("just before the absolute expiry: %v", err)
	}
	e.clock.Advance(2 * time.Hour)
	if _, err := e.svc.Authenticate(e.ctx, res.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after 90 days: err = %v, want ErrNotFound", err)
	}

	// A session idle for more than 30 days expires.
	res2 := e.mustLogin("alice")
	e.clock.Advance(30*day + time.Minute)
	if _, err := e.svc.Authenticate(e.ctx, res2.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("idle 30 days: err = %v, want ErrNotFound", err)
	}
}

func TestSessionIdleDaysSetting(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	if err := e.settings.Set(e.ctx, audit.System, settings.KeySessionIdleDays, 7); err != nil {
		t.Fatal(err)
	}
	res := e.mustLogin("alice")
	if got, want := res.Session.IdleExpiresAt-res.Session.CreatedAt, (7 * 24 * time.Hour).Milliseconds(); got != want {
		t.Errorf("idle window = %dms, want 7 days", got)
	}
}

func TestLastSeenThrottled(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	lastSeen := func() int64 {
		var v int64
		if err := e.db.Read().QueryRow(`SELECT last_seen_at FROM sessions WHERE user_id = ?`, res.User.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	created := lastSeen()
	e.clock.Advance(59 * time.Second)
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	if got := lastSeen(); got != created {
		t.Errorf("last_seen_at moved after 59s: %d -> %d", created, got)
	}
	e.clock.Advance(2 * time.Second)
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	if got, want := lastSeen(), e.clock.Now().UnixMilli(); got != want {
		t.Errorf("last_seen_at = %d after 61s, want %d", got, want)
	}
}

func TestLoginRotatesSession(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	first := e.mustLogin("alice")
	res, err := e.svc.Login(e.ctx, LoginInput{Username: "alice", Password: goodPassword, ReplaceToken: first.Token})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == first.Token {
		t.Fatal("token reused")
	}
	if _, err := e.svc.Authenticate(e.ctx, first.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("old session still valid: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Errorf("new session invalid: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	if err := e.svc.Logout(e.ctx, res.Token, "192.168.1.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, res.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("session valid after logout: %v", err)
	}
	if e.auditCount(audit.ActionLogout) != 1 || e.eventCount("session.revoked") != 1 {
		t.Errorf("logout audit=%d events=%d", e.auditCount(audit.ActionLogout), e.eventCount("session.revoked"))
	}
	if err := e.svc.Logout(e.ctx, res.Token, ""); err != nil {
		t.Errorf("second logout: %v", err)
	}
	if err := e.svc.Logout(e.ctx, "", ""); err != nil {
		t.Errorf("empty logout: %v", err)
	}
	if e.auditCount(audit.ActionLogout) != 1 {
		t.Error("no-op logout was audited")
	}
}

func TestLoginFailuresAndLockout(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)

	// Unknown user and wrong password are indistinguishable.
	_, errUnknown := e.login("nobody", goodPassword)
	_, errWrong := e.login("alice", "wrong password!")
	if !errors.Is(errUnknown, ErrInvalidCredentials) || !errors.Is(errWrong, ErrInvalidCredentials) {
		t.Fatalf("errors = %v, %v", errUnknown, errWrong)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.login("alice", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+3, err)
		}
	}
	// 4 failures for alice so far; the 5th still reaches the password check ...
	if _, err := e.login("alice", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("5th failure: %v", err)
	}
	// ... after which even the correct password is refused with a wait.
	_, err := e.login("alice", goodPassword)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 || rl.RetryAfter > LoginWindow {
		t.Fatalf("after 5 failures: err = %v", err)
	}
	if got := e.auditCount(audit.ActionLoginFailure); got != 6 {
		t.Errorf("%d login failures audited, want 6 (blocked attempts are not audited)", got)
	}
	// The block lifts when the window passes.
	e.clock.Advance(LoginWindow + time.Second)
	if _, err := e.login("alice", goodPassword); err != nil {
		t.Errorf("after the window: %v", err)
	}
	if e.auditCount(audit.ActionLoginSuccess) != 1 {
		t.Error("login success not audited")
	}
}

func TestLoginIPLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < LoginIPMaxFailures; i++ {
		name := "user" + string(rune('a'+i))
		if _, err := e.svc.Login(e.ctx, LoginInput{Username: name, Password: "wrong password!", IP: "192.168.1.77"}); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	_, err := e.svc.Login(e.ctx, LoginInput{Username: "fresh", Password: goodPassword, IP: "192.168.1.77"})
	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("21st attempt from one IP: %v", err)
	}
	if _, err := e.svc.Login(e.ctx, LoginInput{Username: "fresh", Password: goodPassword, IP: "192.168.1.78"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("other IP: %v", err)
	}
}

func TestLoginAuditNeverStoresPasswordsInUsernameField(t *testing.T) {
	e := newEnv(t)
	typed := "My Secret Pass 123!"
	if _, err := e.login(typed, "x"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal(err)
	}
	if d := e.auditDetail(audit.ActionLoginFailure); strings.Contains(d, "Secret") || strings.Contains(strings.ToLower(d), "secret") {
		t.Errorf("audit detail leaks the typed text: %s", d)
	}
}

func TestLoginRefusesDeactivatedAndAppAccounts(t *testing.T) {
	e := newEnv(t)
	e.addUser("gone", RoleMember)
	e.addUser("bot", RoleMember)
	e.execSQL(`UPDATE users SET deactivated_at = ? WHERE username = 'gone'`, e.clock.Now().UnixMilli())
	e.execSQL(`UPDATE users SET kind = 'app' WHERE username = 'bot'`)
	for _, name := range []string{"gone", "bot"} {
		if _, err := e.login(name, goodPassword); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAuthenticateRejectsDeactivatedUser(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	res := e.mustLogin("alice")
	e.execSQL(`UPDATE users SET deactivated_at = ?`, e.clock.Now().UnixMilli())
	if _, err := e.svc.Authenticate(e.ctx, res.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("deactivated user's session: %v", err)
	}
}

func TestLoginRehashesOutdatedParams(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	old := NewHasher(Params{Time: 1, MemoryKiB: 16, Threads: 1, SaltLen: 16, KeyLen: 32}, 0)
	hash, _ := old.Hash(e.ctx, goodPassword)
	e.execSQL(`UPDATE users SET pw_hash = ?`, hash)
	e.mustLogin("alice")
	var got string
	if err := e.db.Read().QueryRow(`SELECT pw_hash FROM users`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if e.svc.hasher.NeedsRehash(got) {
		t.Errorf("hash not upgraded: %s", got)
	}
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	e.addUser("bob", RoleMember)
	phone, laptop := e.mustLogin("alice"), e.mustLogin("alice")
	bobSession := e.mustLogin("bob")

	in := ChangePasswordInput{UserID: alice.ID, Current: goodPassword, New: "a brand new passphrase", KeepSession: laptop.Session.IDHash, IP: "192.168.1.10"}

	bad := in
	bad.Current = "not my password"
	if err := e.svc.ChangePassword(e.ctx, bad); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong current: %v", err)
	}
	weak := in
	weak.New = "1234567890"
	var ve *ValidationError
	if err := e.svc.ChangePassword(e.ctx, weak); !errors.As(err, &ve) || ve.Fields["new"] == "" {
		t.Errorf("common new password: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, phone.Token); err != nil {
		t.Fatalf("failed attempts revoked a session: %v", err)
	}

	if err := e.svc.ChangePassword(e.ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, laptop.Token); err != nil {
		t.Errorf("current session was revoked: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, phone.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("other session survived the password change: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, bobSession.Token); err != nil {
		t.Errorf("another user's session was revoked: %v", err)
	}
	if _, err := e.login("alice", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, err := e.login("alice", in.New); err != nil {
		t.Errorf("new password: %v", err)
	}
	if e.auditCount(audit.ActionPasswordChange) != 1 {
		t.Error("password change not audited")
	}
}

func TestChangePasswordRateLimited(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	in := ChangePasswordInput{UserID: alice.ID, Current: "wrong password!", New: "a brand new passphrase"}
	for i := 0; i < LoginUserMaxFailures; i++ {
		if err := e.svc.ChangePassword(e.ctx, in); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var rl *RateLimitedError
	in.Current = goodPassword
	if err := e.svc.ChangePassword(e.ctx, in); !errors.As(err, &rl) {
		t.Errorf("6th attempt: %v", err)
	}
}

func TestListAndRevokeSessions(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	e.addUser("bob", RoleMember)
	a1, a2 := e.mustLogin("alice"), e.mustLogin("alice")
	b1 := e.mustLogin("bob")

	list, err := e.svc.ListSessions(e.ctx, alice.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSessions = %d sessions, %v", len(list), err)
	}
	if list[0].UserAgent != "test-agent" || list[0].IP != "192.168.1.10" {
		t.Errorf("session = %+v", list[0])
	}

	actor := audit.User(alice.ID, "192.168.1.10")
	if err := e.svc.RevokeSession(e.ctx, actor, alice.ID, b1.Session.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking bob's session as alice: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, b1.Token); err != nil {
		t.Errorf("bob's session was touched: %v", err)
	}
	if err := e.svc.RevokeSession(e.ctx, actor, alice.ID, strings.ToUpper(a1.Session.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(e.ctx, a1.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked session valid: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, a2.Token); err != nil {
		t.Errorf("other session revoked: %v", err)
	}
	if e.auditCount(audit.ActionSessionRevoke) != 1 || e.eventCount("session.revoked") != 1 {
		t.Errorf("audit=%d events=%d", e.auditCount(audit.ActionSessionRevoke), e.eventCount("session.revoked"))
	}
}

func TestPurgeExpired(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	e.mustLogin("alice")
	e.clock.Advance(31 * 24 * time.Hour)
	live := e.mustLogin("alice")
	n, err := e.svc.PurgeExpired(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v", n, err)
	}
	if _, err := e.svc.Authenticate(e.ctx, live.Token); err != nil {
		t.Errorf("live session purged: %v", err)
	}
}

func TestRegister(t *testing.T) {
	e := newEnv(t)
	in := RegisterInput{Username: "Newbie", Password: goodPassword, DisplayName: "New B", IP: "192.168.1.5"}
	if _, err := e.svc.Register(e.ctx, in); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("closed: %v", err)
	}
	if err := e.settings.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Register(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Username != "newbie" || res.User.Role != RoleMember || res.User.DisplayName != "New B" {
		t.Errorf("user = %+v", res.User)
	}
	if _, err := e.svc.Register(e.ctx, in); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate: %v", err)
	}
	bad := in
	bad.Username, bad.Password = "x", "short"
	var ve *ValidationError
	if _, err := e.svc.Register(e.ctx, bad); !errors.As(err, &ve) || ve.Fields["username"] == "" || ve.Fields["password"] == "" {
		t.Errorf("invalid: %v", err)
	}
	if e.auditCount(audit.ActionRegister) != 1 {
		t.Error("registration not audited")
	}
}
