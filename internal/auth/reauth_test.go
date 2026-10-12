package auth

import (
	"errors"
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
