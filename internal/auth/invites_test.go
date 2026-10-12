package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
)

func (e *testEnv) accept(code, username string) (*SessionResult, error) {
	return e.svc.AcceptInvite(e.ctx, AcceptInviteInput{
		Code: code, Username: username, Password: goodPassword, DisplayName: username, UserAgent: "test-agent", IP: "192.168.1.20",
	})
}

func TestInviteLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("admin", RoleAdmin)
	actor := audit.User(admin.ID, "192.168.1.2")

	inv, code, err := e.svc.CreateInvite(e.ctx, actor, CreateInviteInput{Role: RoleGuest, Note: " for Sam "})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Role != RoleGuest || inv.Note != "for Sam" || inv.MaxUses != 1 || inv.CreatedBy != admin.ID {
		t.Errorf("invite = %+v", inv)
	}
	if got, want := inv.ExpiresAt-inv.CreatedAt, DefaultInviteExpiry.Milliseconds(); got != want {
		t.Errorf("expiry = %dms, want 72h", got)
	}
	if len(code) != 52 {
		t.Errorf("code = %q", code)
	}
	var stored []byte
	if err := e.db.Read().QueryRow(`SELECT code_hash FROM invites WHERE id = ?`, inv.ID).Scan(&stored); err != nil || string(stored) != string(HashToken(code)) {
		t.Errorf("code_hash = %x, %v; want SHA-256 of the code", stored, err)
	}
	if st, err := e.svc.LookupInvite(e.ctx, code); err != nil || !st.Valid || st.Role != RoleGuest {
		t.Errorf("LookupInvite = %+v, %v", st, err)
	}
	if st, _ := e.svc.LookupInvite(e.ctx, "nope"); st.Valid {
		t.Error("unknown code is valid")
	}

	res, err := e.accept(code, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Role != RoleGuest || res.User.Username != "sam" {
		t.Errorf("user = %+v", res.User)
	}
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Errorf("session invalid: %v", err)
	}
	// Single use: now exhausted.
	if st, _ := e.svc.LookupInvite(e.ctx, code); st.Valid {
		t.Error("used-up invite still valid")
	}
	if _, err := e.accept(code, "second"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("second accept: %v", err)
	}
	for _, a := range []string{audit.ActionInviteCreate, audit.ActionInviteAccept} {
		if e.auditCount(a) != 1 {
			t.Errorf("audit %s = %d", a, e.auditCount(a))
		}
	}
}

func TestInviteExpiry(t *testing.T) {
	e := newEnv(t)
	_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour - time.Minute)
	if st, _ := e.svc.LookupInvite(e.ctx, code); !st.Valid {
		t.Fatal("invite expired early")
	}
	e.clock.Advance(2 * time.Minute)
	if st, _ := e.svc.LookupInvite(e.ctx, code); st.Valid {
		t.Error("expired invite is valid")
	}
	if _, err := e.accept(code, "late"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("accept after expiry: %v", err)
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 0 {
		t.Error("user created from an expired invite")
	}
}

func TestInviteMaxUses(t *testing.T) {
	e := newEnv(t)
	inv, code, err := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{MaxUses: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if _, err := e.accept(code, name); err != nil {
			t.Fatalf("accept %s: %v", name, err)
		}
	}
	if _, err := e.accept(code, "three"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("third accept: %v", err)
	}
	list, _ := e.svc.ListInvites(e.ctx)
	if len(list) != 1 || list[0].ID != inv.ID || list[0].UseCount != 2 {
		t.Errorf("ListInvites = %+v", list)
	}
}

func TestInviteRevoke(t *testing.T) {
	e := newEnv(t)
	inv, code, _ := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{MaxUses: 5})
	if err := e.svc.RevokeInvite(e.ctx, audit.Console, inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RevokeInvite(e.ctx, audit.Console, inv.ID); err != nil {
		t.Errorf("second revoke: %v", err)
	}
	if err := e.svc.RevokeInvite(e.ctx, audit.Console, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown invite: %v", err)
	}
	if st, _ := e.svc.LookupInvite(e.ctx, code); st.Valid {
		t.Error("revoked invite is valid")
	}
	if _, err := e.accept(code, "late"); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("accept after revoke: %v", err)
	}
	if e.auditCount(audit.ActionInviteRevoke) != 1 {
		t.Errorf("revocations audited = %d, want 1", e.auditCount(audit.ActionInviteRevoke))
	}
}

func TestInviteUsernameTakenConsumesNoUse(t *testing.T) {
	e := newEnv(t)
	e.addUser("taken", RoleMember)
	_, code, _ := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{})
	if _, err := e.accept(code, "TAKEN"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := e.svc.LookupInvite(e.ctx, code); !st.Valid {
		t.Error("failed accept consumed the invite")
	}
	if _, err := e.accept(code, "fresh"); err != nil {
		t.Errorf("retry: %v", err)
	}
}

func TestInviteValidation(t *testing.T) {
	e := newEnv(t)
	var ve *ValidationError
	for name, in := range map[string]CreateInviteInput{
		"role":       {Role: "root"},
		"expires_in": {ExpiresIn: 91 * 24 * time.Hour},
		"max_uses":   {MaxUses: MaxInviteUses + 1},
	} {
		if _, _, err := e.svc.CreateInvite(e.ctx, audit.Console, in); !errors.As(err, &ve) || ve.Fields[name] == "" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, code, _ := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{})
	bad := AcceptInviteInput{Code: code, Username: "A!", Password: ""}
	if _, err := e.svc.AcceptInvite(e.ctx, bad); !errors.As(err, &ve) || ve.Fields["username"] == "" || ve.Fields["password"] == "" {
		t.Errorf("accept validation: %v", err)
	}
	if st, _ := e.svc.LookupInvite(e.ctx, code); !st.Valid {
		t.Error("validation failure consumed the invite")
	}
}
