package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
)

func TestPasswordResetLifecycle(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	old := e.mustLogin("alice")

	code, expires, err := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := expires.Sub(e.clock.Now()); got != ResetTTL {
		t.Errorf("reset lifetime = %v, want 24h", got)
	}
	var stored []byte
	if err := e.db.Read().QueryRow(`SELECT code_hash FROM password_resets`).Scan(&stored); err != nil || string(stored) != string(HashToken(code)) {
		t.Errorf("stored code_hash = %x, %v; want SHA-256 of the code", stored, err)
	}

	// A weak password is rejected without burning the code.
	var ve *ValidationError
	if err := e.svc.ResetPassword(e.ctx, code, "1234567890", ""); !errors.As(err, &ve) || ve.Fields["password"] == "" {
		t.Fatalf("weak password: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, code, "a brand new passphrase", "192.168.1.9"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login("alice", "a brand new passphrase"); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if _, err := e.login("alice", goodPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still works: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, old.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("session survived the reset: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, code, "yet another passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("code reused: %v", err)
	}
	if e.auditCount(audit.ActionResetCreate) != 1 || e.auditCount(audit.ActionPasswordReset) != 1 {
		t.Errorf("audit create=%d reset=%d", e.auditCount(audit.ActionResetCreate), e.auditCount(audit.ActionPasswordReset))
	}
}

func TestPasswordResetExpiry(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	code, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	e.clock.Advance(ResetTTL + time.Second)
	if err := e.svc.ResetPassword(e.ctx, code, "a brand new passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("expired code: %v", err)
	}
}

func TestNewResetSupersedesOld(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	first, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	second, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	if err := e.svc.ResetPassword(e.ctx, first, "a brand new passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("superseded code: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, second, "a brand new passphrase", ""); err != nil {
		t.Errorf("latest code: %v", err)
	}
}

func TestResetRefusesUnknownAndDeactivatedUsers(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	if _, _, err := e.svc.CreateReset(e.ctx, audit.Console, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	code, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	e.execSQL(`UPDATE users SET deactivated_at = ?`, e.clock.Now().UnixMilli())
	if err := e.svc.ResetPassword(e.ctx, code, "a brand new passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("deactivated user: %v", err)
	}
	if _, _, err := e.svc.CreateReset(e.ctx, audit.Console, alice.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("create for deactivated user: %v", err)
	}
	if err := e.svc.ResetPassword(e.ctx, "", "a brand new passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("empty code: %v", err)
	}
}

func TestPasswordChangeRetiresPendingReset(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	code, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
	in := ChangePasswordInput{UserID: alice.ID, Current: goodPassword, New: "a brand new passphrase"}
	if err := e.svc.ChangePassword(e.ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.ResetPassword(e.ctx, code, "yet another passphrase", ""); !errors.Is(err, ErrResetInvalid) {
		t.Errorf("reset code survived a password change: %v", err)
	}
}
