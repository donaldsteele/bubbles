package search

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a store, a chat service and a search service (dates in UTC).
type env struct {
	t     *testing.T
	ctx   context.Context
	db    *store.DB
	chat  *chat.Service
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
	cs := chat.New(db, settings.New(db))
	cs.SetFileAttacher(fakeAttacher{})
	return &env{t: t, ctx: context.Background(), db: db, chat: cs, svc: New(db, cs, time.UTC), clock: clock}
}

// user is a test account plus the Actor that represents it.
type user struct {
	ID    string
	Name  string
	Actor chat.Actor
}

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
	return user{ID: id, Name: name, Actor: chat.Actor{ID: id, Role: role, Kind: kind}}
}

func (e *env) human(name, role string) user { return e.addUser(name, role, protocol.KindHuman) }

func (e *env) channel(owner user, name, kind string, members ...user) protocol.Channel {
	e.t.Helper()
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ID
	}
	ch, err := e.chat.CreateChannel(e.ctx, owner.Actor, chat.CreateChannelParams{Name: name, Kind: kind, MemberIDs: ids})
	if err != nil {
		e.t.Fatalf("create channel %s: %v", name, err)
	}
	return ch
}

func (e *env) join(u user, ch protocol.Channel) {
	e.t.Helper()
	if _, err := e.chat.JoinChannel(e.ctx, u.Actor, ch.ID); err != nil {
		e.t.Fatalf("join %s: %v", *ch.Name, err)
	}
}

// addMember inserts a membership row directly (used for app accounts).
func (e *env) addMember(u user, ch protocol.Channel) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO memberships (channel_id, user_id, role, joined_at) VALUES (?, ?, 'member', ?)`, ch.ID, u.ID, tx.Now())
		return err
	})
	if err != nil {
		e.t.Fatalf("add member: %v", err)
	}
}

func (e *env) post(u user, channelID, body string, fileIDs ...string) protocol.Message {
	e.t.Helper()
	m, _, err := e.chat.CreateMessage(e.ctx, u.Actor, chat.CreateMessageParams{ChannelID: channelID, Body: body, FileIDs: fileIDs})
	if err != nil {
		e.t.Fatalf("post %q: %v", body, err)
	}
	return m
}

func (e *env) reply(u user, channelID, rootID, body string) protocol.Message {
	e.t.Helper()
	m, _, err := e.chat.CreateMessage(e.ctx, u.Actor, chat.CreateMessageParams{ChannelID: channelID, Body: body, ThreadRootID: rootID})
	if err != nil {
		e.t.Fatalf("reply %q: %v", body, err)
	}
	return m
}

func (e *env) system(channelID, authorID, body string) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := e.chat.CreateMessageTx(tx, chat.CreateMessageParams{
			ChannelID: channelID, AuthorID: authorID, Kind: protocol.MessageSystem, Body: body,
		})
		return err
	})
	if err != nil {
		e.t.Fatalf("system message: %v", err)
	}
}

// search runs a first-page search and fails the test on error.
func (e *env) search(u user, q string) protocol.SearchPage {
	e.t.Helper()
	page, err := e.svc.Search(e.ctx, u.Actor, Params{Q: q})
	if err != nil {
		e.t.Fatalf("search %q: %v", q, err)
	}
	return page
}

// bodies lists the message bodies of a page, in order.
func bodies(p protocol.SearchPage) []string {
	out := make([]string, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.Message.Body
	}
	return out
}

func wantBodies(t *testing.T, what string, p protocol.SearchPage, want ...string) {
	t.Helper()
	got := bodies(p)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s: bodies = %q, want %q", what, got, want)
	}
}

// wantSet compares bodies ignoring order.
func wantSet(t *testing.T, what string, p protocol.SearchPage, want ...string) {
	t.Helper()
	got := append([]string(nil), bodies(p)...)
	w := append([]string(nil), want...)
	slices.Sort(got)
	slices.Sort(w)
	if strings.Join(got, "\x00") != strings.Join(w, "\x00") {
		t.Errorf("%s: bodies = %q, want %q", what, bodies(p), want)
	}
}

// fakeAttacher stands in for the files service: file ids starting with "img"
// are images, all others plain files.
type fakeAttacher struct{}

func (fakeAttacher) AttachTx(tx *store.Tx, messageID, authorID string, fileIDs []string) ([]protocol.File, error) {
	var out []protocol.File
	for i, id := range fileIDs {
		category, mime := protocol.FileOther, "text/plain"
		if strings.HasPrefix(id, "img") {
			category, mime = protocol.FileImage, "image/png"
		}
		name := "file-" + id
		_, err := tx.Exec(`INSERT INTO files (id, sha256, size, mime, category, original_name, uploader_id, created_at)
			VALUES (?, ?, 5, ?, ?, ?, ?, ?)`, id, strings.Repeat("a", 64), mime, category, name, authorID, tx.Now())
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO attachments (message_id, file_id, position) VALUES (?, ?, ?)`, messageID, id, i); err != nil {
			return nil, err
		}
		out = append(out, protocol.File{ID: id, Name: name, Size: 5, Mime: mime, Category: category, URL: protocol.FileURL(id, name)})
	}
	return out, nil
}
