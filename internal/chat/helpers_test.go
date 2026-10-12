package chat

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
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
	c.t = c.t.Add(time.Millisecond)
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
	st    *settings.Store
	svc   *Service
	clock *fakeClock
}

func newEnv(t *testing.T) *env {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), store.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	st := settings.New(db)
	return &env{t: t, ctx: context.Background(), db: db, st: st, svc: New(db, st), clock: clock}
}

// user is a test account plus the Actor that represents it.
type user struct {
	ID    string
	Name  string
	Actor Actor
}

// addUser inserts an account directly (the auth service owns real creation).
func (e *env) addUser(name, role, kind string) user {
	e.t.Helper()
	var id string
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		id = tx.NewID()
		_, err := tx.Exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, name, strings.ToUpper(name[:1])+name[1:], kind, role, tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatalf("add user %s: %v", name, err)
	}
	return user{ID: id, Name: name, Actor: Actor{ID: id, Role: role, Kind: kind}}
}

func (e *env) human(name, role string) user { return e.addUser(name, role, protocol.KindHuman) }

func (e *env) deactivate(u user) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE users SET deactivated_at = ? WHERE id = ?`, tx.Now(), u.ID)
		return err
	})
	if err != nil {
		e.t.Fatalf("deactivate: %v", err)
	}
}

func (e *env) channel(owner user, name, kind string, members ...user) protocol.Channel {
	e.t.Helper()
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}
	ch, err := e.svc.CreateChannel(e.ctx, owner.Actor, CreateChannelParams{Name: name, Kind: kind, MemberIDs: ids})
	if err != nil {
		e.t.Fatalf("create channel %s: %v", name, err)
	}
	return ch
}

func (e *env) post(u user, channelID, body string) protocol.Message {
	e.t.Helper()
	m, _, err := e.svc.CreateMessage(e.ctx, u.Actor, CreateMessageParams{ChannelID: channelID, Body: body})
	if err != nil {
		e.t.Fatalf("post %q: %v", body, err)
	}
	return m
}

func (e *env) reply(u user, channelID, rootID, body string) protocol.Message {
	e.t.Helper()
	m, _, err := e.svc.CreateMessage(e.ctx, u.Actor, CreateMessageParams{ChannelID: channelID, Body: body, ThreadRootID: rootID})
	if err != nil {
		e.t.Fatalf("reply %q: %v", body, err)
	}
	return m
}

// logged is one persisted event.
type logged struct {
	Seq     int64
	Type    string
	Channel string
	User    string
	Payload string
}

func (e *env) events(afterSeq int64) []logged {
	e.t.Helper()
	rows, err := e.db.Read().QueryContext(e.ctx,
		`SELECT seq, type, COALESCE(channel_id, ''), COALESCE(user_id, ''), payload FROM events WHERE seq > ? ORDER BY seq`, afterSeq)
	if err != nil {
		e.t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var out []logged
	for rows.Next() {
		var l logged
		if err := rows.Scan(&l.Seq, &l.Type, &l.Channel, &l.User, &l.Payload); err != nil {
			e.t.Fatalf("scan event: %v", err)
		}
		out = append(out, l)
	}
	return out
}

func (e *env) head() int64 {
	e.t.Helper()
	var seq int64
	err := e.db.Read().QueryRowContext(e.ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'events'), 0)`).Scan(&seq)
	if err != nil {
		e.t.Fatalf("event head: %v", err)
	}
	return seq
}

func types(evs []logged) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func wantTypes(t *testing.T, got []logged, want ...string) {
	t.Helper()
	g := strings.Join(types(got), ",")
	w := strings.Join(want, ",")
	if g != w {
		t.Fatalf("events = [%s], want [%s]", g, w)
	}
}

// setRetentionCeiling sets the server-wide default retention, which also caps
// per-channel retention.
func (e *env) setRetentionCeiling(days int) error {
	return e.st.Set(e.ctx, audit.System, settings.KeyDefaultRetentionDays, days)
}

// fakeAttacher stands in for the files service: it inserts file rows and the
// attachments for the message, like the real one will.
type fakeAttacher struct {
	calls int
	err   error
}

func (f *fakeAttacher) AttachTx(tx *store.Tx, messageID, authorID string, fileIDs []string) ([]protocol.File, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	var out []protocol.File
	for i, id := range fileIDs {
		name := "file-" + id + ".txt"
		_, err := tx.Exec(`INSERT INTO files (id, sha256, size, mime, category, original_name, uploader_id, created_at)
			VALUES (?, ?, 5, 'text/plain', 'file', ?, ?, ?)`, id, strings.Repeat("a", 64), name, authorID, tx.Now())
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO attachments (message_id, file_id, position) VALUES (?, ?, ?)`, messageID, id, i); err != nil {
			return nil, err
		}
		out = append(out, protocol.File{ID: id, Name: name, Size: 5, Mime: "text/plain", Category: protocol.FileOther, URL: protocol.FileURL(id, name)})
	}
	return out, nil
}
