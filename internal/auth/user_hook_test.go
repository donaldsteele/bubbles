package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// userHook records the users OnUserCreated saw. It also checks that each new
// user row is already visible inside the transaction, and returns err.
type userHook struct {
	err  error
	seen []string
}

func (h *userHook) run(_ context.Context, tx *store.Tx, userID string) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errors.New("new user row not visible inside the transaction")
	}
	h.seen = append(h.seen, userID)
	return h.err
}

// createHuman creates one human account through path: "setup", "invite" or
// "register".
func createHuman(e *testEnv, path string) (*SessionResult, error) {
	e.t.Helper()
	switch path {
	case "setup":
		token, err := e.svc.InitSetup(e.ctx)
		if err != nil {
			e.t.Fatal(err)
		}
		return e.svc.Setup(e.ctx, goodSetup(token))
	case "invite":
		_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, CreateInviteInput{})
		if err != nil {
			e.t.Fatal(err)
		}
		return e.accept(code, "sam")
	case "register":
		if err := e.settings.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
			e.t.Fatal(err)
		}
		return e.svc.Register(e.ctx, RegisterInput{Username: "sam", Password: goodPassword, UserAgent: "test-agent", IP: "192.168.1.3"})
	}
	e.t.Fatalf("unknown path %q", path)
	return nil, nil
}

var humanCreationPaths = []string{"setup", "invite", "register"}

func TestOnUserCreatedRunsInEveryHumanCreation(t *testing.T) {
	for _, path := range humanCreationPaths {
		t.Run(path, func(t *testing.T) {
			hook := &userHook{}
			e := newEnv(t, envOpts{onUserCreated: hook.run})
			res, err := createHuman(e, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(hook.seen) != 1 || hook.seen[0] != res.User.ID {
				t.Errorf("hook saw %v, want [%s]", hook.seen, res.User.ID)
			}
		})
	}
}

func TestOnUserCreatedErrorAbortsTheTransaction(t *testing.T) {
	boom := errors.New("boom")
	for _, path := range humanCreationPaths {
		t.Run(path, func(t *testing.T) {
			e := newEnv(t, envOpts{onUserCreated: (&userHook{err: boom}).run})
			if _, err := createHuman(e, path); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the hook error", err)
			}
			if n := e.count(`SELECT COUNT(*) FROM users`); n != 0 {
				t.Errorf("%d users committed although the hook failed", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM sessions`); n != 0 {
				t.Errorf("%d sessions committed although the hook failed", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM invites WHERE use_count > 0`); n != 0 {
				t.Errorf("%d invite uses committed although the hook failed", n)
			}
		})
	}
}

func TestOnUserCreatedNilIsNoOp(t *testing.T) {
	for _, path := range humanCreationPaths {
		t.Run(path, func(t *testing.T) {
			if _, err := createHuman(newEnv(t), path); err != nil {
				t.Errorf("without a hook: %v", err)
			}
		})
	}
}
