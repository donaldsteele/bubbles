package events

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type world struct {
	db      *store.DB
	hub     *Hub
	clock   *fakeClock
	markers atomic.Int64
}

// nextMarker returns a unique seq for hub-published (ephemeral-path) marker
// events, far above any stored seq.
func (w *world) nextMarker() int64 { return 1<<40 + w.markers.Add(1) }

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{db: openDB(t), clock: &fakeClock{t: testNow}}
	w.hub = NewHub(HubOptions{DB: w.db, Now: w.clock.Now})
	t.Cleanup(w.hub.Close)
	return w
}

func (w *world) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := w.db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func (w *world) user(t *testing.T, id, role string) {
	t.Helper()
	w.exec(t, `INSERT INTO users (id, username, kind, role, created_at) VALUES (?, ?, 'human', ?, 0)`, id, id, role)
}

func (w *world) channel(t *testing.T, id, kind string, members ...string) {
	t.Helper()
	var name any
	if kind == protocol.ChannelPublic || kind == protocol.ChannelPrivate {
		name = id
	}
	w.exec(t, `INSERT INTO channels (id, kind, name, created_at) VALUES (?, ?, ?, 0)`, id, kind, name)
	for _, m := range members {
		w.exec(t, `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES (?, ?, 0)`, id, m)
	}
}

// commit appends evs in one transaction, running pre first, and returns
// their seqs.
func (w *world) commit(t *testing.T, pre func(tx *store.Tx) error, evs ...Event) []int64 {
	t.Helper()
	var seqs []int64
	err := w.db.Do(context.Background(), func(tx *store.Tx) error {
		seqs = seqs[:0]
		if pre != nil {
			if err := pre(tx); err != nil {
				return err
			}
		}
		for _, e := range evs {
			seq, err := Append(tx, e)
			if err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return seqs
}

type closeCall struct {
	code   int
	reason string
}

type testSub struct {
	*Subscription
	closed chan closeCall
}

func (w *world) subscribe(id, role, session string) *testSub {
	ts := &testSub{closed: make(chan closeCall, 1)}
	ts.Subscription = w.hub.Subscribe(Viewer{UserID: id, Role: role, Kind: protocol.KindHuman}, session,
		func(code int, reason string) { ts.closed <- closeCall{code, reason} })
	return ts
}

func recvFrame(t *testing.T, s *testSub) protocol.Event {
	t.Helper()
	select {
	case f := <-s.Frames():
		var e protocol.Event
		if err := json.Unmarshal(f.Data, &e); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if e.Seq != f.Seq {
			t.Fatalf("frame seq %d, envelope seq %d", f.Seq, e.Seq)
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5s")
	}
	return protocol.Event{}
}

// marker publishes a settings.updated event every viewer sees.
func (w *world) marker(t *testing.T) int64 {
	t.Helper()
	return w.commit(t, nil, Event{Type: TypeSettingsUpdated, Payload: json.RawMessage(`{"keys":["marker"]}`)})[0]
}

// until returns "type@channel" (or "type#user") labels of the frames
// received before the frame with seq marker, skipping presence frames.
func until(t *testing.T, s *testSub, marker int64) []string {
	t.Helper()
	var got []string
	for {
		e := recvFrame(t, s)
		if e.Seq == marker {
			return got
		}
		if e.Type == TypePresenceChanged {
			continue
		}
		got = append(got, label(e))
	}
}

func label(e protocol.Event) string {
	if e.ChannelID != "" {
		return e.Type + "@" + e.ChannelID
	}
	var p struct {
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(e.Data, &p)
	if p.ID != "" {
		return e.Type + "#" + p.ID
	}
	if p.UserID != "" {
		return e.Type + "#" + p.UserID
	}
	return e.Type
}

func ev(typ, channel, user, payload string) Event {
	if payload == "" {
		payload = "{}"
	}
	return Event{Type: typ, ChannelID: channel, UserID: user, Payload: json.RawMessage(payload)}
}

func TestHubDeliversOnlyVisibleEvents(t *testing.T) {
	w := newWorld(t)
	for _, u := range []struct{ id, role string }{
		{"alice", protocol.RoleMember}, {"bob", protocol.RoleMember},
		{"carol", protocol.RoleMember}, {"gus", protocol.RoleGuest}, {"dave", protocol.RoleMember},
	} {
		w.user(t, u.id, u.role)
	}
	w.channel(t, "pub", protocol.ChannelPublic, "alice", "gus")
	w.channel(t, "priv", protocol.ChannelPrivate, "alice", "bob")
	w.channel(t, "dm", protocol.ChannelDM, "alice", "carol")
	subs := map[string]*testSub{
		"alice": w.subscribe("alice", protocol.RoleMember, ""),
		"bob":   w.subscribe("bob", protocol.RoleMember, ""),
		"carol": w.subscribe("carol", protocol.RoleMember, ""),
		"gus":   w.subscribe("gus", protocol.RoleGuest, ""),
		"dave":  w.subscribe("dave", protocol.RoleMember, ""),
	}

	w.commit(t, nil,
		ev(TypeMessageCreated, "priv", "", `{"id":"m1"}`),
		ev(TypeMessageCreated, "dm", "", `{"id":"m2"}`),
		ev(TypeMessageCreated, "pub", "", `{"id":"m3"}`),
		ev(TypeChannelCreated, "priv", "", ""),
		ev(TypeChannelCreated, "pub", "", ""),
		ev(TypeChannelUpdated, "dm", "", ""),
		ev(TypeReactionAdded, "dm", "", ""),
		ev(TypeReadUpdated, "", "alice", ""),
		ev(TypeMembershipUpdated, "", "bob", ""),
		ev(TypeUserUpdated, "", "carol", `{"id":"carol"}`),
		ev(TypeUserUpdated, "", "alice", `{"id":"alice"}`),
	)
	m := w.marker(t)
	want := map[string][]string{
		"alice": {"message.created@priv", "message.created@dm", "message.created@pub", "channel.created@priv",
			"channel.created@pub", "channel.updated@dm", "reaction.added@dm", "read.updated",
			"user.updated#carol", "user.updated#alice"},
		"bob":   {"message.created@priv", "channel.created@priv", "channel.created@pub", "membership.updated", "user.updated#carol", "user.updated#alice"},
		"carol": {"message.created@dm", "channel.created@pub", "channel.updated@dm", "reaction.added@dm", "user.updated#carol", "user.updated#alice"},
		"gus":   {"message.created@pub", "channel.created@pub", "user.updated#alice"},
		"dave":  {"channel.created@pub", "user.updated#carol", "user.updated#alice"},
	}
	for name, s := range subs {
		if got := until(t, s, m); !slices.Equal(got, want[name]) {
			t.Errorf("%s got %v\nwant %v", name, got, want[name])
		}
	}

	// bob leaves priv, dave joins it: the cache follows, and the departing
	// user still gets member_left.
	w.commit(t, func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM memberships WHERE channel_id = 'priv' AND user_id = 'bob'`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('priv', 'dave', 0)`)
		return err
	},
		ev(TypeChannelMemberLeft, "priv", "", `{"channel_id":"priv","user_id":"bob"}`),
		ev(TypeChannelMemberJoined, "priv", "", `{"channel_id":"priv","user_id":"dave"}`),
		ev(TypeMessageCreated, "priv", "", `{"id":"m4"}`),
	)
	m = w.marker(t)
	want = map[string][]string{
		"alice": {"channel.member_left@priv", "channel.member_joined@priv", "message.created@priv"},
		"bob":   {"channel.member_left@priv"},
		"carol": nil,
		"gus":   nil,
		"dave":  {"channel.member_left@priv", "channel.member_joined@priv", "message.created@priv"},
	}
	for name, s := range subs {
		if got := until(t, s, m); !slices.Equal(got, want[name]) {
			t.Errorf("after membership change %s got %v\nwant %v", name, got, want[name])
		}
	}
}

func TestRollbackPublishesNothing(t *testing.T) {
	w := newWorld(t)
	s := w.subscribe("alice", protocol.RoleMember, "")
	errBoom := errors.New("boom")
	err := w.db.Do(context.Background(), func(tx *store.Tx) error {
		if _, err := Append(tx, ev(TypeSettingsUpdated, "", "", `{"keys":["rolled-back"]}`)); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("Do = %v, want boom", err)
	}
	m := w.marker(t)
	if got := until(t, s, m); len(got) != 0 {
		t.Fatalf("frames before marker = %v, want none", got)
	}
}

func TestPublishOrderMatchesSeqUnderConcurrentWriters(t *testing.T) {
	w := newWorld(t)
	s := w.subscribe("alice", protocol.RoleMember, "")
	const writers, txs = 8, 40
	total := 0
	for i := range writers * txs {
		total += 1 + i%3
	}

	got := make(chan []int64, 1)
	go func() {
		var seqs []int64
		for len(seqs) < total {
			select {
			case f := <-s.Frames():
				if f.Seq > 0 {
					seqs = append(seqs, f.Seq)
				}
			case <-time.After(10 * time.Second):
				got <- seqs
				return
			}
		}
		got <- seqs
	}()

	var wg sync.WaitGroup
	for g := range writers {
		wg.Go(func() {
			for i := range txs {
				n := 1 + (g*txs+i)%3
				err := w.db.Do(context.Background(), func(tx *store.Tx) error {
					for range n {
						if _, err := Append(tx, ev(TypeSettingsUpdated, "", "", `{"keys":["k"]}`)); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					t.Errorf("Do: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	seqs := <-got
	if len(seqs) != total {
		t.Fatalf("received %d events, want %d", len(seqs), total)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("frame %d has seq %d, want %d (publish order must equal seq order)", i, seq, i+1)
		}
	}
}

func TestSlowConsumerClosedWith4008(t *testing.T) {
	w := newWorld(t)
	s := w.subscribe("alice", protocol.RoleMember, "")
	<-s.Frames() // own presence
	for i := range QueueSize + 10 {
		w.hub.Publish(Event{Seq: int64(i + 1), Type: TypeSettingsUpdated, Payload: json.RawMessage(`{}`)})
	}
	select {
	case c := <-s.closed:
		if c.code != CloseSlowConsumer || c.reason != ReasonSlowConsumer {
			t.Fatalf("close = %+v, want 4008 slow_consumer", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow consumer was not closed")
	}
	if n := w.hub.Subscribers("alice"); n != 0 {
		t.Fatalf("subscribers after close = %d, want 0", n)
	}
	// The queue holds a gap-free prefix and nothing after the overflow.
	for i := range QueueSize {
		f := <-s.Frames()
		if f.Seq != int64(i+1) {
			t.Fatalf("queued frame %d has seq %d", i, f.Seq)
		}
	}
	w.hub.Publish(Event{Seq: 9999, Type: TypeSettingsUpdated})
	select {
	case f := <-s.Frames():
		t.Fatalf("frame %d delivered after close", f.Seq)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionRevokedAndDeactivationCloseSubscriptions(t *testing.T) {
	w := newWorld(t)
	s1 := w.subscribe("alice", protocol.RoleMember, "s1")
	s2 := w.subscribe("alice", protocol.RoleMember, "s2")
	s3 := w.subscribe("alice", protocol.RoleMember, "s3")
	other := w.subscribe("bob", protocol.RoleMember, "s9")

	expectClosed := func(s *testSub, name string) {
		t.Helper()
		select {
		case c := <-s.closed:
			if c.code != CloseSessionRevoked {
				t.Fatalf("%s closed with %d, want 4009", name, c.code)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s not closed within 1s", name)
		}
	}
	expectOpen := func(s *testSub, name string) {
		t.Helper()
		select {
		case c := <-s.closed:
			t.Fatalf("%s closed unexpectedly (%d)", name, c.code)
		default:
		}
	}

	w.commit(t, nil, ev(TypeSessionRevoked, "", "alice", `{"session_id":"s1"}`))
	expectClosed(s1, "s1")
	w.marker(t)
	w.commit(t, nil, ev(TypeSessionRevoked, "", "alice", `{"all":true,"except_session_id":"s3"}`))
	expectClosed(s2, "s2")
	m := w.marker(t)
	until(t, s3, m)
	expectOpen(s3, "s3")
	w.commit(t, nil, ev(TypeUserUpdated, "", "alice", `{"id":"alice","deactivated":true}`))
	expectClosed(s3, "s3")
	m = w.marker(t)
	until(t, other, m)
	expectOpen(other, "bob")
}

func TestEncodeFrame(t *testing.T) {
	data, err := EncodeFrame(Event{Seq: 7, Type: TypeMessageCreated, ChannelID: "c1", CreatedAt: testNow.UnixMilli(), Payload: json.RawMessage(`{"id":"m1"}`)})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"seq":7,"type":"message.created","channel_id":"c1","ts":"2026-03-01T12:00:00.000Z","data":{"id":"m1"}}`
	if string(data) != want {
		t.Fatalf("frame = %s\nwant  %s", data, want)
	}
	data, _ = EncodeFrame(Event{Type: TypeTyping, CreatedAt: testNow.UnixMilli()})
	if want := `{"seq":0,"type":"typing","ts":"2026-03-01T12:00:00.000Z","data":{}}`; string(data) != want {
		t.Fatalf("ephemeral frame = %s", data)
	}
}

func TestPublishAfterCloseIsDropped(t *testing.T) {
	w := newWorld(t)
	w.hub.Close()
	w.hub.Close()
	w.marker(t) // must not block or panic
}
