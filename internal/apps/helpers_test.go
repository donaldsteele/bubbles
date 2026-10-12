package apps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	chat  *chat.Service
	svc   *Service
	clock *fakeClock
	admin chat.Actor
	aud   audit.Actor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), store.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cs := chat.New(db, settings.New(db))
	e := &env{
		t: t, ctx: context.Background(), db: db, chat: cs, clock: clock,
		svc: New(Options{DB: db, Chat: cs, Now: clock.Now}),
	}
	id := e.addUser("root", protocol.RoleAdmin)
	e.admin = chat.Actor{ID: id, Role: protocol.RoleAdmin, Kind: protocol.KindHuman}
	e.aud = audit.User(id, "192.168.1.2")
	return e
}

// addUser inserts a human account directly.
func (e *env) addUser(name, role string) string {
	e.t.Helper()
	var id string
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		id = tx.NewID()
		_, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, 'human', ?, ?)`,
			id, name, name, role, tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatalf("add user %s: %v", name, err)
	}
	return id
}

func (e *env) channel(name, kind string, members ...string) protocol.Channel {
	e.t.Helper()
	ch, err := e.chat.CreateChannel(e.ctx, e.admin, chat.CreateChannelParams{Name: name, Kind: kind, MemberIDs: members})
	if err != nil {
		e.t.Fatalf("create channel %s: %v", name, err)
	}
	return ch
}

func (e *env) app(name string) protocol.App {
	e.t.Helper()
	a, err := e.svc.CreateApp(e.ctx, e.aud, CreateAppInput{Name: name, OwnerID: e.admin.ID})
	if err != nil {
		e.t.Fatalf("create app %s: %v", name, err)
	}
	return a
}

func (e *env) token(appID string, scopes ...string) IssuedToken {
	e.t.Helper()
	tok, err := e.svc.IssueToken(e.ctx, e.aud, appID, IssueTokenInput{Scopes: scopes})
	if err != nil {
		e.t.Fatalf("issue token: %v", err)
	}
	return tok
}

// authenticate runs the bearer authenticator on a request carrying header.
func (e *env) authenticate(header string) (*auth.Principal, error) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/apps/self", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	return e.svc.Authenticator().Authenticate(req)
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}
