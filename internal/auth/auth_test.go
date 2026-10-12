package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// cheapParams keeps argon2id fast; one test uses DefaultParams.
var cheapParams = Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}

const goodPassword = "correct horse battery"

// fakeClock is a manually advanced clock shared by the store and the service.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
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

type testEnv struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	settings *settings.Store
	svc      *Service
	clock    *fakeClock
	dataDir  string
}

type envOpts struct {
	onSetup       func(ctx context.Context, tx *store.Tx, adminID string) error
	onUserCreated func(ctx context.Context, tx *store.Tx, userID string) error
}

func newEnv(t *testing.T, o ...envOpts) *testEnv {
	t.Helper()
	var opt envOpts
	if len(o) > 0 {
		opt = o[0]
	}
	clock := newFakeClock()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := settings.New(db)
	svc, err := New(Options{
		DB: db, Settings: st, DataDir: dir, Now: clock.Now,
		Params: cheapParams, OnSetup: opt.onSetup, OnUserCreated: opt.onUserCreated,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	return &testEnv{t: t, ctx: context.Background(), db: db, settings: st, svc: svc, clock: clock, dataDir: dir}
}

// addUser inserts a user directly with the cheap-param hash of goodPassword.
func (e *testEnv) addUser(username string, role Role) *User {
	e.t.Helper()
	hash, err := e.svc.HashPassword(e.ctx, goodPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	var u *User
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		var err error
		u, err = insertUser(tx, newUserFields{username: username, displayName: username, password: goodPassword}, role, hash)
		return err
	})
	if err != nil {
		e.t.Fatalf("addUser: %v", err)
	}
	return u
}

func (e *testEnv) login(username, password string) (*SessionResult, error) {
	return e.svc.Login(e.ctx, LoginInput{Username: username, Password: password, UserAgent: "test-agent", IP: "192.168.1.10"})
}

func (e *testEnv) mustLogin(username string) *SessionResult {
	e.t.Helper()
	res, err := e.login(username, goodPassword)
	if err != nil {
		e.t.Fatalf("login %s: %v", username, err)
	}
	return res
}

func (e *testEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (e *testEnv) auditCount(action string) int {
	return e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action)
}

func (e *testEnv) eventCount(typ string) int {
	return e.count(`SELECT COUNT(*) FROM events WHERE type = ?`, typ)
}

func (e *testEnv) execSQL(query string, args ...any) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

func (e *testEnv) auditDetail(action string) string {
	e.t.Helper()
	var d string
	err := e.db.Read().QueryRow(`SELECT detail FROM audit_log WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&d)
	if err != nil && err != sql.ErrNoRows {
		e.t.Fatal(err)
	}
	return d
}
