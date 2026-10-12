package notify

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

var testNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// fakes ------------------------------------------------------------------

type fakeSettings struct {
	mu      sync.Mutex
	enabled bool
	payload string
}

func (s *fakeSettings) WebPushEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

func (s *fakeSettings) PushPayload() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payload
}

type fakePresence struct {
	mu      sync.Mutex
	online  map[string]bool
	focused map[string]bool
	within  []time.Duration
}

func (p *fakePresence) Online(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.online[id]
}

func (p *fakePresence) UserFocusedRecently(id string, within time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.within = append(p.within, within)
	return p.focused[id]
}

type sent struct {
	userID string
	n      push.Notification
}

type fakePusher struct {
	mu    sync.Mutex
	subs  map[string]bool
	sends []sent
	// asked counts HasSubscriptions calls.
	asked int
}

func (p *fakePusher) HasSubscriptions(_ context.Context, id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	return p.subs[id]
}

func (p *fakePusher) Send(id string, n push.Notification) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends = append(p.sends, sent{id, n})
}

func (p *fakePusher) got() []sent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sent(nil), p.sends...)
}

func (p *fakePusher) users() []string {
	var out []string
	for _, s := range p.got() {
		out = append(out, s.userID)
	}
	slices.Sort(out)
	return out
}

// world -------------------------------------------------------------------

type world struct {
	t        *testing.T
	ctx      context.Context
	db       *store.DB
	settings *fakeSettings
	presence *fakePresence
	pusher   *fakePusher
	d        *Dispatcher
}

func newWorld(t *testing.T) *world {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		store.Options{Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	w := &world{
		t: t, ctx: context.Background(), db: db,
		settings: &fakeSettings{enabled: true, payload: settings.PushMinimal},
		presence: &fakePresence{online: map[string]bool{}, focused: map[string]bool{}},
		pusher:   &fakePusher{subs: map[string]bool{}},
	}
	w.d = New(Options{
		DB: db, Settings: w.settings, Presence: w.presence, Pusher: w.pusher,
		Now: func() time.Time { return testNow },
	})
	t.Cleanup(w.d.Close)
	return w
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	err := w.db.Do(w.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	})
	if err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
}

// user adds a user; its push subscription is simulated through the fake pusher.
func (w *world) user(id, kind string, subscribed bool) {
	w.t.Helper()
	w.exec(`INSERT INTO users (id, username, display_name, kind, role, created_at) VALUES (?, ?, ?, ?, 'member', 1)`,
		id, id, "Name of "+id, kind)
	w.pusher.subs[id] = subscribed
}

func (w *world) channel(id, kind, name string) {
	w.t.Helper()
	var nm, dmKey any
	if kind == "dm" || kind == "group_dm" {
		dmKey = id
	} else {
		nm = name
	}
	w.exec(`INSERT INTO channels (id, kind, name, dm_key, created_at) VALUES (?, ?, ?, ?, 1)`, id, kind, nm, dmKey)
}

// member joins userID to channelID. level "" keeps the per-kind default.
func (w *world) member(channelID, userID, level, minSeverity string, mutedUntil *int64) {
	w.t.Helper()
	var lv any
	if level != "" {
		lv = level
	}
	if minSeverity == "" {
		minSeverity = "warning"
	}
	w.exec(`INSERT INTO memberships (channel_id, user_id, joined_at, notify_level, alert_min_severity, muted_until) VALUES (?, ?, 1, ?, ?, ?)`,
		channelID, userID, lv, minSeverity, mutedUntil)
}

func (w *world) message(id, channelID, authorID, kind, body, plain string) {
	w.t.Helper()
	w.exec(`INSERT INTO messages (id, channel_id, author_id, kind, body, body_plain, created_at) VALUES (?, ?, ?, ?, ?, ?, 1)`,
		id, channelID, authorID, kind, body, plain)
}

func msgEvent(seq int64, m protocol.Message) events.Event {
	raw, _ := json.Marshal(m)
	return events.Event{Seq: seq, Type: events.TypeMessageCreated, ChannelID: m.ChannelID, Payload: raw}
}

func alertEvent(seq int64, m protocol.Message, transition string) events.Event {
	raw, _ := json.Marshal(protocol.AlertUpdatedEvent{Message: m, Transition: transition})
	return events.Event{Seq: seq, Type: events.TypeAlertUpdated, ChannelID: m.ChannelID, Payload: raw}
}

func textMsg(id, channelID, authorID, body string) protocol.Message {
	return protocol.Message{
		ID: id, ChannelID: channelID, Kind: protocol.MessageText, Body: body,
		Author: protocol.Author{ID: authorID, Username: authorID, DisplayName: "Name of " + authorID, Kind: "human"},
	}
}

func alertMsg(id, channelID, authorID, severity string) protocol.Message {
	m := textMsg(id, channelID, authorID, "disk almost full")
	m.Kind = protocol.MessageAlert
	m.Author.Kind = "app"
	m.Alert = &protocol.Alert{Severity: severity, State: protocol.AlertOpen, Title: "Disk almost full on nas"}
	return m
}

func ms(t time.Time) *int64 { v := t.UnixMilli(); return &v }

// a standard channel: author alice; bob mentions-level; carol all; dave none.
func (w *world) standard() {
	for _, u := range []string{"alice", "bob", "carol", "dave"} {
		w.user(u, "human", true)
	}
	w.channel("ch", "public", "ops")
	w.member("ch", "alice", "", "", nil)
	w.member("ch", "bob", "mentions", "", nil)
	w.member("ch", "carol", "all", "", nil)
	w.member("ch", "dave", "none", "", nil)
}

// tests -------------------------------------------------------------------

func TestNotifyAppliesTheRulesToEveryMemberButTheAuthor(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.user("erin", "human", true) // muted into the future, level all
	w.channel("other", "public", "other")
	w.member("ch", "erin", "all", "", ms(testNow.Add(time.Hour)))
	w.user("frank", "human", true) // expired mute, level all
	w.member("ch", "frank", "all", "", ms(testNow.Add(-time.Hour)))
	w.user("botty", "app", true) // bot member, never notified
	w.member("ch", "botty", "all", "", nil)
	w.user("gone", "human", true) // deactivated account
	w.member("ch", "gone", "all", "", nil)
	w.exec(`UPDATE users SET deactivated_at = 5 WHERE id = 'gone'`)
	w.user("outsider", "human", true) // not in the channel
	w.member("other", "outsider", "all", "", nil)

	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "plain chatter")))
	if got, want := w.pusher.users(), []string{"carol", "frank"}; !slices.Equal(got, want) {
		t.Fatalf("pushed to %v, want %v (level all and an expired mute only)", got, want)
	}
	n := w.pusher.got()[0].n
	if n.ChannelID != "ch" || n.ChannelName != "ops" || n.ChannelKind != "public" || n.MessageID != "m1" || n.SenderName != "Name of alice" || n.Critical {
		t.Errorf("notification = %+v", n)
	}
	if n.Body != "" {
		t.Errorf("minimal mode put the body %q into the notification", n.Body)
	}
}

func TestNotifyMentionsReachMentionsLevelMembers(t *testing.T) {
	w := newWorld(t)
	w.standard()
	m := textMsg("m1", "ch", "alice", "hi @bob @dave")
	m.Mentions = protocol.Mentions{UserIDs: []string{"bob", "dave"}}
	w.d.Handle(w.ctx, msgEvent(1, m))
	// bob by mention, carol by level all; dave opted out entirely.
	if got, want := w.pusher.users(), []string{"bob", "carol"}; !slices.Equal(got, want) {
		t.Fatalf("pushed to %v, want %v", got, want)
	}

	w.pusher.sends = nil
	m = textMsg("m2", "ch", "alice", "@channel standup")
	m.Mentions = protocol.Mentions{Channel: true}
	w.d.Handle(w.ctx, msgEvent(2, m))
	if got, want := w.pusher.users(), []string{"bob", "carol"}; !slices.Equal(got, want) {
		t.Errorf("@channel pushed to %v, want %v", got, want)
	}
}

func TestNotifyHereNeedsAnOnlineRecipient(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.user("erin", "human", true)
	w.member("ch", "erin", "mentions", "", nil)
	w.presence.online["bob"] = true // erin is offline
	m := textMsg("m1", "ch", "alice", "@here deploy now")
	m.Mentions = protocol.Mentions{Here: true}
	w.d.Handle(w.ctx, msgEvent(1, m))
	if got, want := w.pusher.users(), []string{"bob", "carol"}; !slices.Equal(got, want) {
		t.Fatalf("@here pushed to %v, want online bob and level-all carol only", got)
	}
}

func TestNotifyDirectMessagesNotifyByDefault(t *testing.T) {
	w := newWorld(t)
	w.user("alice", "human", true)
	w.user("bob", "human", true)
	w.channel("dm1", "dm", "")
	w.member("dm1", "alice", "", "", nil) // the per-kind default is "all"
	w.member("dm1", "bob", "", "", nil)
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "dm1", "alice", "psst")))
	got := w.pusher.got()
	if len(got) != 1 || got[0].userID != "bob" {
		t.Fatalf("sends = %+v, want one to bob", got)
	}
	if n := got[0].n; n.ChannelKind != "dm" || n.ChannelName != "" || push.Title(n) != "Name of alice" {
		t.Errorf("dm notification = %+v (title %q)", n, push.Title(n))
	}
}

func TestNotifyFocusedUserGetsNoPush(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.presence.focused["carol"] = true
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "hello")))
	if got := w.pusher.got(); len(got) != 0 {
		t.Fatalf("pushed to %v although the only notifiable user has a visible window", w.pusher.users())
	}
	for _, within := range w.presence.within {
		if within != 60*time.Second {
			t.Errorf("focus window = %v, want 60s", within)
		}
	}
	if len(w.presence.within) == 0 {
		t.Error("focus was never consulted")
	}
	// Once the window is gone the same event pushes.
	w.presence.focused["carol"] = false
	w.d.Handle(w.ctx, msgEvent(2, textMsg("m2", "ch", "alice", "hello again")))
	if got := w.pusher.users(); !slices.Equal(got, []string{"carol"}) {
		t.Errorf("pushed to %v after the window closed, want carol", got)
	}
}

func TestNotifyFocusIsNotConsultedForUsersTheRulesExclude(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "carol", "i wrote this")))
	// bob (mentions, no mention) and dave (none) are excluded by the rules, carol is the author.
	if len(w.presence.within) != 0 || w.pusher.asked != 0 || len(w.pusher.got()) != 0 {
		t.Errorf("rules-excluded users reached focus/subscription checks: focus=%d asked=%d sends=%d",
			len(w.presence.within), w.pusher.asked, len(w.pusher.got()))
	}
}

func TestNotifyOnlySubscribedUsersGetPush(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.pusher.subs["carol"] = false
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "hello")))
	if got := w.pusher.got(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none: carol has no subscription", got)
	}
}

func TestNotifyDisabledWebPushDoesNothing(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.settings.enabled = false
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "hello")))
	if len(w.pusher.got()) != 0 || w.pusher.asked != 0 || len(w.presence.within) != 0 {
		t.Errorf("web push disabled but the dispatcher worked: sends=%d asked=%d focus=%d",
			len(w.pusher.got()), w.pusher.asked, len(w.presence.within))
	}
}

func TestNotifySystemMessagesAndOtherEventsNeverNotify(t *testing.T) {
	w := newWorld(t)
	w.standard()
	sys := textMsg("m1", "ch", "alice", "Resolved by netdiag")
	sys.Kind = protocol.MessageSystem
	w.d.Handle(w.ctx, msgEvent(1, sys))
	for _, typ := range []string{events.TypeMessageUpdated, events.TypeReactionAdded, events.TypeChannelCreated, events.TypeTyping} {
		raw, _ := json.Marshal(textMsg("m2", "ch", "alice", "edited"))
		w.d.Handle(w.ctx, events.Event{Seq: 2, Type: typ, ChannelID: "ch", Payload: raw})
	}
	w.d.Handle(w.ctx, events.Event{Seq: 3, Type: events.TypeMessageCreated, Payload: json.RawMessage(`{"not":"a message"`)})
	w.d.Handle(w.ctx, events.Event{Seq: 4, Type: events.TypeAlertUpdated, Payload: json.RawMessage(`[]`)})
	deleted := textMsg("m3", "ch", "alice", "gone")
	deleted.Deleted = true
	w.d.Handle(w.ctx, msgEvent(5, deleted))
	if got := w.pusher.got(); len(got) != 0 {
		t.Fatalf("sends = %+v, want none", got)
	}
}

func TestNotifyAlertsFollowSeverityAndTransition(t *testing.T) {
	w := newWorld(t)
	w.standard() // bob: mentions/warning, carol: all/warning, dave: none
	w.user("erin", "human", true)
	w.member("ch", "erin", "all", "critical", nil)
	w.user("nas", "app", false)
	w.member("ch", "nas", "", "", nil)

	// A new critical alert: carol (all), erin (all, critical) and alice and bob
	// (critical passes the mentions level). dave opted out.
	w.d.Handle(w.ctx, msgEvent(1, alertMsg("a1", "ch", "nas", protocol.SeverityCritical)))
	if got, want := w.pusher.users(), []string{"alice", "bob", "carol", "erin"}; !slices.Equal(got, want) {
		t.Fatalf("critical alert pushed to %v, want %v", got, want)
	}
	for _, s := range w.pusher.got() {
		if !s.n.Critical || s.n.SenderName != "Name of nas" || s.n.MessageID != "a1" {
			t.Errorf("notification = %+v", s.n)
		}
	}

	// A new warning alert: carol only (bob's mentions level blocks it, erin's
	// threshold is critical).
	w.pusher.sends = nil
	w.d.Handle(w.ctx, msgEvent(2, alertMsg("a2", "ch", "nas", protocol.SeverityWarning)))
	if got, want := w.pusher.users(), []string{"carol"}; !slices.Equal(got, want) {
		t.Errorf("warning alert pushed to %v, want %v", got, want)
	}
	if w.pusher.got()[0].n.Critical {
		t.Error("a warning alert was marked critical")
	}

	// alert.updated: only escalation and renotify_due notify.
	cases := []struct {
		transition string
		severity   string
		want       []string
	}{
		{protocol.AlertTransitionEscalated, protocol.SeverityCritical, []string{"alice", "bob", "carol", "erin"}},
		{protocol.AlertTransitionRenotifyDue, protocol.SeverityWarning, []string{"carol"}},
		{protocol.AlertTransitionCreated, protocol.SeverityWarning, []string{"carol"}},
		{protocol.AlertTransitionUpdatedOther, protocol.SeverityCritical, nil},
		{protocol.AlertTransitionAcked, protocol.SeverityCritical, nil},
		{protocol.AlertTransitionResolved, protocol.SeverityCritical, nil},
		{"", protocol.SeverityCritical, nil},
	}
	for i, tc := range cases {
		w.pusher.sends = nil
		w.d.Handle(w.ctx, alertEvent(int64(10+i), alertMsg("a1", "ch", "nas", tc.severity), tc.transition))
		if got := w.pusher.users(); !slices.Equal(got, tc.want) {
			t.Errorf("alert.updated %q/%s pushed to %v, want %v", tc.transition, tc.severity, got, tc.want)
		}
	}
}

func TestNotifyMutedMemberStillMissesCriticalAlerts(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.user("erin", "human", true) // mentions level, muted for an hour
	w.member("ch", "erin", "mentions", "", ms(testNow.Add(time.Hour)))
	w.user("nas", "app", false)
	w.member("ch", "nas", "", "", nil)
	w.d.Handle(w.ctx, msgEvent(1, alertMsg("a1", "ch", "nas", protocol.SeverityCritical)))
	if got, want := w.pusher.users(), []string{"alice", "bob", "carol"}; !slices.Equal(got, want) {
		t.Fatalf("critical alert pushed to %v, want %v (muted erin gets nothing)", got, want)
	}
}

func TestNotifyFullPayloadCarriesThePlainBody(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.settings.payload = settings.PushFull
	w.message("m1", "ch", "alice", "text", "**bold** claim", "bold claim")
	w.d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "**bold** claim")))
	got := w.pusher.got()
	if len(got) != 1 || got[0].n.Body != "bold claim" {
		t.Fatalf("sends = %+v, want one with the plain body", got)
	}

	// A message the database does not know falls back to the event's body.
	w.pusher.sends = nil
	w.d.Handle(w.ctx, msgEvent(2, textMsg("m-unknown", "ch", "alice", "raw *text*")))
	if got := w.pusher.got(); len(got) != 1 || got[0].n.Body != "raw *text*" {
		t.Errorf("fallback sends = %+v", got)
	}

	// Alerts show their title.
	w.pusher.sends = nil
	w.user("nas", "app", false)
	w.member("ch", "nas", "", "", nil)
	w.d.Handle(w.ctx, msgEvent(3, alertMsg("a1", "ch", "nas", protocol.SeverityCritical)))
	if got := w.pusher.got(); len(got) == 0 || got[0].n.Body != "Disk almost full on nas" {
		t.Errorf("alert sends = %+v, want the alert title as body", got)
	}
}

func TestNotifyUsesTheWebhookNameAsSender(t *testing.T) {
	w := newWorld(t)
	w.standard()
	m := textMsg("m1", "ch", "alice", "from a hook")
	name := "Grafana"
	m.OverrideName = &name
	w.d.Handle(w.ctx, msgEvent(1, m))
	if got := w.pusher.got(); len(got) != 1 || got[0].n.SenderName != "Grafana" {
		t.Errorf("sends = %+v, want sender Grafana", got)
	}
}

// the queue ---------------------------------------------------------------

func TestNotifyPublishNeverBlocksAndDropsOnOverflow(t *testing.T) {
	w := newWorld(t)
	d := New(Options{DB: w.db, Settings: w.settings, Presence: w.presence, Pusher: w.pusher, QueueSize: 3})
	defer d.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for seq := int64(1); seq <= 10; seq++ { // never started: nothing drains the queue
			d.Publish(msgEvent(seq, textMsg("m", "ch", "alice", "x")))
		}
		d.Publish(events.Event{Type: events.TypeTyping}) // ignored, not counted
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full queue")
	}
	if got := d.Dropped(); got != 7 {
		t.Errorf("Dropped = %d, want 7", got)
	}
}

func TestNotifyDispatchesInSeqOrderThroughTheQueue(t *testing.T) {
	w := newWorld(t)
	w.standard()
	w.d.Start()
	w.d.Start() // idempotent
	const n = 50
	for seq := int64(1); seq <= n; seq++ {
		w.d.Publish(msgEvent(seq, textMsg("m"+itoa(seq), "ch", "alice", "x")))
		w.d.Publish(events.Event{Seq: seq, Type: events.TypeReactionAdded, ChannelID: "ch"}) // ignored
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(w.pusher.got()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d notifications arrived", len(w.pusher.got()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i, s := range w.pusher.got() {
		if want := "m" + itoa(int64(i+1)); s.n.MessageID != want || s.userID != "carol" {
			t.Fatalf("notification %d = %s to %s, want %s to carol", i, s.n.MessageID, s.userID, want)
		}
	}
	w.d.Close()
	w.d.Close() // idempotent
	w.d.Publish(msgEvent(99, textMsg("late", "ch", "alice", "x")))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestNotifyAPanickingPusherDoesNotKillTheDispatcher(t *testing.T) {
	w := newWorld(t)
	w.standard()
	var calls atomic.Int32
	d := New(Options{DB: w.db, Settings: w.settings, Presence: w.presence, Pusher: panicPusher{&calls}})
	d.Start()
	defer d.Close()
	d.Publish(msgEvent(1, textMsg("m1", "ch", "alice", "x")))
	d.Publish(msgEvent(2, textMsg("m2", "ch", "alice", "x")))
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("pusher called %d times, want 2 (the loop died after a panic)", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type panicPusher struct{ n *atomic.Int32 }

func (p panicPusher) HasSubscriptions(context.Context, string) bool { return true }
func (p panicPusher) Send(string, push.Notification) {
	p.n.Add(1)
	panic("boom")
}

// with the real hub -------------------------------------------------------

// TestNotifyHubFocusSuppressesPush wires the real hub as Presence: a socket
// that reports focus visible=true holds back the push, and so does one that
// went hidden less than a minute ago.
func TestNotifyHubFocusSuppressesPush(t *testing.T) {
	w := newWorld(t)
	w.standard()
	clock := &steppingClock{t: testNow}
	hub := events.NewHub(events.HubOptions{DB: w.db, Now: clock.Now})
	defer hub.Close()
	d := New(Options{DB: w.db, Settings: w.settings, Presence: hub, Pusher: w.pusher, Now: clock.Now})
	defer d.Close()
	events.SetPublisher(w.db, events.NewMultiPublisher(hub, d))

	sub := hub.Subscribe(events.Viewer{UserID: "carol", Role: protocol.RoleMember, Kind: "human"}, "", nil)
	defer sub.Leave()
	sub.Focus(true)
	d.Handle(w.ctx, msgEvent(1, textMsg("m1", "ch", "alice", "hello")))
	if got := w.pusher.got(); len(got) != 0 {
		t.Fatalf("pushed to %v while carol's window is visible", w.pusher.users())
	}

	sub.Focus(false)
	clock.advance(30 * time.Second)
	d.Handle(w.ctx, msgEvent(2, textMsg("m2", "ch", "alice", "hello")))
	if got := w.pusher.got(); len(got) != 0 {
		t.Fatalf("pushed to %v 30s after the window was hidden", w.pusher.users())
	}

	clock.advance(31 * time.Second)
	d.Handle(w.ctx, msgEvent(3, textMsg("m3", "ch", "alice", "hello")))
	if got := w.pusher.users(); !slices.Equal(got, []string{"carol"}) {
		t.Fatalf("pushed to %v 61s after the window was hidden, want carol", got)
	}
}

// TestNotifyHubPublishesIntoTheDispatcher: an event appended in a transaction
// reaches the dispatcher through the MultiPublisher installed on the DB.
func TestNotifyHubPublishesIntoTheDispatcher(t *testing.T) {
	w := newWorld(t)
	w.standard()
	hub := events.NewHub(events.HubOptions{DB: w.db})
	defer hub.Close()
	d := New(Options{DB: w.db, Settings: w.settings, Presence: hub, Pusher: w.pusher})
	d.Start()
	defer d.Close()
	events.SetPublisher(w.db, events.NewMultiPublisher(hub, d))

	raw, _ := json.Marshal(textMsg("m1", "ch", "alice", "through the log"))
	err := w.db.Do(w.ctx, func(tx *store.Tx) error {
		_, err := events.Append(tx, events.Event{Type: events.TypeMessageCreated, ChannelID: "ch", Payload: raw})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(w.pusher.got()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the appended event never reached the dispatcher")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.pusher.users(); !slices.Equal(got, []string{"carol"}) {
		t.Errorf("pushed to %v, want carol", got)
	}
}

type steppingClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *steppingClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
