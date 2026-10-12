package alerts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/audit"
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
	t      *testing.T
	ctx    context.Context
	db     *store.DB
	chat   *chat.Service
	apps   *apps.Service
	svc    *Service
	clock  *fakeClock
	admin  chat.Actor
	alice  chat.Actor
	bot    chat.Actor // app "netdiag" with alerts:write, a member of #ops
	botApp protocol.App
	ops    protocol.Channel
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
	cs := chat.New(db, st)
	e := &env{
		t: t, ctx: context.Background(), db: db, chat: cs, clock: clock,
		apps: apps.New(apps.Options{DB: db, Chat: cs, Now: clock.Now}),
		svc:  New(Options{DB: db, Chat: cs, Settings: st}),
	}
	e.admin = e.human("root", protocol.RoleAdmin)
	e.alice = e.human("alice", protocol.RoleMember)
	e.ops = e.channel("ops", e.alice.ID)
	e.botApp, e.bot = e.newApp("netdiag", e.ops.ID)
	return e
}

func (e *env) human(name, role string) chat.Actor {
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
	return chat.Actor{ID: id, Role: role, Kind: protocol.KindHuman}
}

func (e *env) channel(name string, members ...string) protocol.Channel {
	e.t.Helper()
	ch, err := e.chat.CreateChannel(e.ctx, e.admin, chat.CreateChannelParams{Name: name, Kind: protocol.ChannelPublic, MemberIDs: members})
	if err != nil {
		e.t.Fatalf("create channel %s: %v", name, err)
	}
	return ch
}

// newApp creates an app whose bot joins the channels and returns its actor
// with the alerts:write scope.
func (e *env) newApp(name string, channels ...string) (protocol.App, chat.Actor) {
	e.t.Helper()
	app, err := e.apps.CreateApp(e.ctx, audit.System, apps.CreateAppInput{Name: name, OwnerID: e.admin.ID})
	if err != nil {
		e.t.Fatalf("create app: %v", err)
	}
	for _, ch := range channels {
		if _, err := e.chat.AddMember(e.ctx, e.admin, ch, app.UserID, protocol.MemberMember); err != nil {
			e.t.Fatalf("add bot: %v", err)
		}
	}
	return app, chat.Actor{ID: app.UserID, Role: protocol.RoleMember, Kind: protocol.KindApp, Scopes: []string{protocol.ScopeAlertsWrite}}
}

// warn is a warning alert for #ops under key "disk".
func warn(mods ...func(*protocol.AlertInput)) protocol.AlertInput {
	in := protocol.AlertInput{Channel: "ops", Severity: protocol.SeverityWarning, Title: "Disk almost full", Text: "92% used", DedupeKey: "disk"}
	for _, m := range mods {
		m(&in)
	}
	return in
}

func (e *env) post(actor chat.Actor, in protocol.AlertInput) PostResult {
	e.t.Helper()
	res, err := e.svc.Post(e.ctx, actor, in)
	if err != nil {
		e.t.Fatalf("post alert: %v", err)
	}
	return res
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// logged is one persisted event.
type logged struct {
	Seq     int64
	Type    string
	Channel string
	Payload json.RawMessage
}

func (e *env) events(types ...string) []logged {
	e.t.Helper()
	rows, err := e.db.Read().Query(`SELECT seq, type, COALESCE(channel_id, ''), payload FROM events ORDER BY seq`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []logged
	for rows.Next() {
		var l logged
		var payload string
		if err := rows.Scan(&l.Seq, &l.Type, &l.Channel, &payload); err != nil {
			e.t.Fatal(err)
		}
		l.Payload = json.RawMessage(payload)
		if len(types) == 0 || contains(types, l.Type) {
			out = append(out, l)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// transitions returns the transition of every alert.updated event, in order.
func (e *env) transitions() []string {
	e.t.Helper()
	var out []string
	for _, l := range e.events("alert.updated") {
		var ev protocol.AlertUpdatedEvent
		if err := json.Unmarshal(l.Payload, &ev); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, ev.Transition)
	}
	return out
}

// alertMessage loads an alert message through the chat service.
func (e *env) alertMessage(id string) protocol.Message {
	e.t.Helper()
	msg, err := e.chat.GetMessage(e.ctx, e.alice, id)
	if err != nil {
		e.t.Fatalf("get message: %v", err)
	}
	return msg
}

func (e *env) aud(a chat.Actor) audit.Actor { return audit.User(a.ID, "192.168.1.2") }

func jsonUnmarshal(b json.RawMessage, v any) error { return json.Unmarshal(b, v) }

// withAllowlist returns the actor with a token channel allowlist (empty, not
// nil, when no id is given).
func withAllowlist(a chat.Actor, ids ...string) chat.Actor {
	a.ChannelIDs = append([]string{}, ids...)
	return a
}

// withScopes returns the actor holding exactly the given token scopes.
func withScopes(a chat.Actor, scopes ...string) chat.Actor {
	a.Scopes = append([]string{}, scopes...)
	return a
}

func updateDisabled(b *bool) apps.UpdateAppInput { return apps.UpdateAppInput{Disabled: b} }
