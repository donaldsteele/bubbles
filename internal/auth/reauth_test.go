package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
)

func TestVerifyPassword(t *testing.T) {
	e := newEnv(t)
	u := e.addUser("owner", RoleAdmin)
	if err := e.svc.VerifyPassword(e.ctx, u.ID, goodPassword); err != nil {
		t.Errorf("right password: %v", err)
	}
	for name, tc := range map[string]struct{ id, pw string }{
		"wrong password": {u.ID, "not the password"},
		"unknown user":   {"01NOBODY", goodPassword},
	} {
		if err := e.svc.VerifyPassword(e.ctx, tc.id, tc.pw); !errors.Is(err, ErrWrongPassword) {
			t.Errorf("%s: %v, want ErrWrongPassword", name, err)
		}
	}
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE users SET deactivated_at = 1 WHERE id = ?`, u.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyPassword(e.ctx, u.ID, goodPassword); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("deactivated account: %v", err)
	}
}

// TestAdoptSetupTokenKeepsTheResetToken: after a factory reset the server
// keeps the token the reset issued; a malformed or missing file gets a new
// one, and with users AdoptSetupToken behaves like InitSetup.
func TestAdoptSetupTokenKeepsTheResetToken(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(e.dataDir, SetupTokenFile)
	issued, err := WriteSetupToken(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.AdoptSetupToken(e.ctx)
	if err != nil || got != issued || !e.svc.setupTokenValid(issued) {
		t.Fatalf("AdoptSetupToken = %q, %v; want the issued %q", got, err, issued)
	}

	if err := os.WriteFile(path, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := e.svc.AdoptSetupToken(e.ctx)
	if err != nil || len(fresh) != 52 || fresh == issued || e.svc.setupTokenValid(issued) {
		t.Fatalf("malformed file: %q, %v", fresh, err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != fresh {
		t.Error("token file not replaced")
	}

	e.addUser("owner", RoleAdmin)
	if tok, err := e.svc.AdoptSetupToken(e.ctx); tok != "" || err != nil {
		t.Errorf("with users: %q, %v", tok, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("token file kept after setup: %v", err)
	}
}
