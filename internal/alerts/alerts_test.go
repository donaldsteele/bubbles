package alerts

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestAlertsHundredIdenticalPostsMakeOneMessage(t *testing.T) {
	e := newEnv(t)
	var first PostResult
	for i := range 100 {
		res := e.post(e.bot, warn())
		if i == 0 {
			first = res
			if !res.Created() {
				t.Fatalf("first transition = %s", res.Transition)
			}
		} else if res.Transition != protocol.AlertTransitionUpdatedOther || res.Message.ID != first.Message.ID {
			t.Fatalf("post %d: transition %s message %s (first %s)", i+1, res.Transition, res.Message.ID, first.Message.ID)
		}
		e.clock.Advance(time.Second)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'alert'`); n != 1 {
		t.Errorf("alert messages = %d, want 1", n)
	}
	msg := e.alertMessage(first.Message.ID)
	if msg.Alert == nil || msg.Alert.Occurrences != 100 || msg.Alert.State != protocol.AlertOpen || msg.Alert.Severity != protocol.SeverityWarning {
		t.Fatalf("alert = %+v", msg.Alert)
	}
	// Exactly one notification-eligible event: the creation. None of the 99 repeats escalates or renotifies.
	created := 0
	for _, l := range e.events("message.created") {
		var m protocol.Message
		if err := jsonUnmarshal(l.Payload, &m); err != nil {
			t.Fatal(err)
		}
		if m.Kind == protocol.MessageAlert {
			created++
		}
	}
	if created != 1 {
		t.Errorf("alert message.created events = %d, want 1", created)
	}
	for _, tr := range e.transitions() {
		if tr == protocol.AlertTransitionEscalated || tr == protocol.AlertTransitionRenotifyDue || tr == protocol.AlertTransitionCreated {
			t.Fatalf("a repeat emitted notification-eligible transition %s", tr)
		}
	}
	if n := len(e.transitions()); n != 99 {
		t.Errorf("alert.updated events = %d, want 99", n)
	}
	if n := len(e.events("message.updated")); n != 99 {
		t.Errorf("message.updated events = %d, want 99", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM alerts`); n != 1 {
		t.Errorf("alerts rows = %d", n)
	}
}

func TestAlertsEscalationRaisesSeverityAndNotifiesOnce(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	e.clock.Advance(time.Minute)
	up := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Severity = protocol.SeverityCritical; in.Title = "Disk full" }))
	if up.Transition != protocol.AlertTransitionEscalated || up.Message.ID != first.Message.ID {
		t.Fatalf("escalation = %s on %s", up.Transition, up.Message.ID)
	}
	if a := up.Message.Alert; a.Severity != protocol.SeverityCritical || a.Title != "Disk full" || a.Occurrences != 2 {
		t.Errorf("alert after escalation = %+v", a)
	}
	var notified int64
	if err := e.db.Read().QueryRow(`SELECT last_notified_at FROM alerts WHERE message_id = ?`, first.Message.ID).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != e.clock.Now().UnixMilli() {
		t.Errorf("last_notified_at = %d, want the escalation time %d", notified, e.clock.Now().UnixMilli())
	}
	// A later warning never lowers the severity and is not an escalation.
	e.clock.Advance(time.Minute)
	down := e.post(e.bot, warn())
	if down.Transition != protocol.AlertTransitionUpdatedOther || down.Message.Alert.Severity != protocol.SeverityCritical {
		t.Errorf("de-escalating repeat = %s severity %s", down.Transition, down.Message.Alert.Severity)
	}
	// The same severity again is not an escalation either.
	if again := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Severity = protocol.SeverityCritical })); again.Transition != protocol.AlertTransitionUpdatedOther {
		t.Errorf("same-severity repeat = %s", again.Transition)
	}
	want := []string{"escalated", "updated_other", "updated_other"}
	if got := e.transitions(); !slices.Equal(got, want) {
		t.Errorf("transitions = %v, want %v", got, want)
	}
}

func TestAlertsRenotifyWhenIntervalPasses(t *testing.T) {
	e := newEnv(t)
	e.post(e.bot, warn())
	e.clock.Advance(1799 * time.Second)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionUpdatedOther {
		t.Fatalf("inside the default 1800 s interval: %s", res.Transition)
	}
	e.clock.Advance(time.Second)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionRenotifyDue {
		t.Fatalf("at 1800 s: %s", res.Transition)
	}
	// Renotifying restarts the interval.
	e.clock.Advance(time.Minute)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionUpdatedOther {
		t.Errorf("right after a renotification: %s", res.Transition)
	}
	e.clock.Advance(1800 * time.Second)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionRenotifyDue {
		t.Errorf("one interval after the renotification: %s", res.Transition)
	}
}

func TestAlertsRenotifyIntervalOverrides(t *testing.T) {
	e := newEnv(t)
	sixty, off := 60, 0
	e.post(e.bot, warn(func(in *protocol.AlertInput) { in.RenotifyAfterS = &sixty }))
	e.clock.Advance(61 * time.Second)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionRenotifyDue {
		t.Errorf("60 s interval kept from the first post: %s", res.Transition)
	}
	// 0 turns renotification off, however long it takes.
	e.post(e.bot, warn(func(in *protocol.AlertInput) { in.RenotifyAfterS = &off }))
	e.clock.Advance(48 * time.Hour)
	if res := e.post(e.bot, warn()); res.Transition != protocol.AlertTransitionUpdatedOther {
		t.Errorf("renotify off: %s", res.Transition)
	}
}

func TestAlertsAckStopsRenotify(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	acked, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), first.Message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a := acked.Alert; a.State != protocol.AlertAcked || a.AckedBy == nil || *a.AckedBy != e.alice.ID || a.AckedAt == nil {
		t.Fatalf("acked alert = %+v", a)
	}
	e.clock.Advance(3 * time.Hour)
	res := e.post(e.bot, warn())
	if res.Transition != protocol.AlertTransitionUpdatedOther || res.Message.Alert.State != protocol.AlertAcked {
		t.Errorf("repeat of an acked alert past the interval: %s state %s", res.Transition, res.Message.Alert.State)
	}
	// An escalation still gets through.
	up := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Severity = protocol.SeverityCritical }))
	if up.Transition != protocol.AlertTransitionEscalated {
		t.Errorf("escalation of an acked alert: %s", up.Transition)
	}
	// Acking again changes nothing and writes no second audit row or event.
	before := len(e.transitions())
	if _, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), first.Message.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.transitions()) != before {
		t.Error("second ack emitted an event")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'alert.ack'`); n != 1 {
		t.Errorf("alert.ack audit rows = %d, want 1", n)
	}
	if got := e.transitions()[0]; got != protocol.AlertTransitionAcked {
		t.Errorf("first alert.updated = %s, want acked", got)
	}
}

func TestAlertsResolveByKeyPostsThreadReply(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	msg, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", "disk is back below 80%")
	if err != nil {
		t.Fatal(err)
	}
	if a := msg.Alert; a.State != protocol.AlertResolved || a.ResolvedBy == nil || *a.ResolvedBy != e.bot.ID || a.ResolvedAt == nil {
		t.Fatalf("resolved alert = %+v", a)
	}
	if msg.ID != first.Message.ID || msg.ReplyCount != 1 {
		t.Errorf("message %s reply_count %d", msg.ID, msg.ReplyCount)
	}
	thread, err := e.chat.ListThread(e.ctx, e.alice, first.Message.ID, chat.PageOptions{})
	if err != nil || len(thread.Replies) != 1 {
		t.Fatalf("thread = %+v, %v", thread, err)
	}
	r := thread.Replies[0]
	if r.Kind != protocol.MessageSystem || r.Body != "Resolved by netdiag: disk is back below 80%" || r.Author.ID != e.bot.ID ||
		r.ThreadRootID == nil || *r.ThreadRootID != first.Message.ID {
		t.Errorf("reply = %+v", r)
	}
	if got := e.transitions(); !slices.Equal(got, []string{"resolved"}) {
		t.Errorf("transitions = %v", got)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'alert.resolve'`); n != 1 {
		t.Errorf("alert.resolve audit rows = %d", n)
	}

	// Resolving again is a no-op: no second reply, event or audit row.
	again, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", "")
	if err != nil || again.ID != msg.ID || again.ReplyCount != 1 || again.Alert.State != protocol.AlertResolved {
		t.Errorf("second resolve = %+v, %v", again, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'system'`); n != 1 {
		t.Errorf("system messages = %d, want 1", n)
	}
	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "never-posted", ""); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("unknown key: %v", err)
	}
	// Without text the reply is just who resolved it.
	e.post(e.bot, warn(func(in *protocol.AlertInput) { in.DedupeKey = "cpu" }))
	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "cpu", ""); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := e.db.Read().QueryRow(`SELECT body FROM messages WHERE kind = 'system' ORDER BY id DESC LIMIT 1`).Scan(&body); err != nil || body != "Resolved by netdiag" {
		t.Errorf("reply without text = %q, %v", body, err)
	}
}

func TestAlertsPostAfterResolveOpensNewAlert(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	e.post(e.bot, warn())
	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", ""); err != nil {
		t.Fatal(err)
	}
	second := e.post(e.bot, warn())
	if !second.Created() || second.Message.ID == first.Message.ID || second.Message.Alert.Occurrences != 1 || second.Message.Alert.State != protocol.AlertOpen {
		t.Fatalf("post after resolve = %s %+v", second.Transition, second.Message.Alert)
	}
	if n := e.count(`SELECT COUNT(*) FROM alerts WHERE state = 'resolved'`); n != 1 {
		t.Errorf("resolved alerts = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM alerts WHERE state != 'resolved'`); n != 1 {
		t.Errorf("unresolved alerts = %d", n)
	}
	// The resolved alert stays resolved; repeats go to the new one.
	if third := e.post(e.bot, warn()); third.Message.ID != second.Message.ID || third.Message.Alert.Occurrences != 2 {
		t.Errorf("repeat after reopening = %s occurrences %d", third.Message.ID, third.Message.Alert.Occurrences)
	}
	old := e.alertMessage(first.Message.ID)
	if old.Alert.State != protocol.AlertResolved || old.Alert.Occurrences != 2 {
		t.Errorf("old alert = %+v", old.Alert)
	}
}

func TestAlertsResolveByIDForMembers(t *testing.T) {
	e := newEnv(t)
	bob := e.human("bob", protocol.RoleMember)
	first := e.post(e.bot, warn())
	if _, err := e.svc.Resolve(e.ctx, bob, e.aud(bob), first.Message.ID, ""); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("non-member resolve: %v", err)
	}
	if _, err := e.svc.Ack(e.ctx, bob, e.aud(bob), first.Message.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("non-member ack: %v", err)
	}
	msg, err := e.svc.Resolve(e.ctx, e.alice, e.aud(e.alice), first.Message.ID, "  fixed it ")
	if err != nil {
		t.Fatal(err)
	}
	if a := msg.Alert; a.State != protocol.AlertResolved || *a.ResolvedBy != e.alice.ID {
		t.Fatalf("alert = %+v", a)
	}
	thread, _ := e.chat.ListThread(e.ctx, e.alice, first.Message.ID, chat.PageOptions{})
	if len(thread.Replies) != 1 || thread.Replies[0].Body != "Resolved by alice: fixed it" {
		t.Errorf("replies = %+v", thread.Replies)
	}
	if _, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), first.Message.ID); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("ack of a resolved alert: %v", err)
	}
	if again, err := e.svc.Resolve(e.ctx, e.alice, e.aud(e.alice), first.Message.ID, "again"); err != nil || again.ReplyCount != 1 {
		t.Errorf("second resolve = %+v, %v", again, err)
	}
	// A text message is not an alert.
	text, _, err := e.chat.CreateMessage(e.ctx, e.alice, chat.CreateMessageParams{ChannelID: e.ops.ID, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Resolve(e.ctx, e.alice, e.aud(e.alice), text.ID, ""); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("resolving a text message: %v", err)
	}
	if _, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), "nope"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("unknown alert: %v", err)
	}
}

func TestAlertsPostPermissions(t *testing.T) {
	e := newEnv(t)
	secret := e.channel("secret")
	_, other := e.newApp("other")

	tests := []struct {
		name   string
		actor  chat.Actor
		in     protocol.AlertInput
		target error
	}{
		{"human", e.alice, warn(), chat.ErrForbidden},
		{"no alerts:write", chat.Actor{ID: e.bot.ID, Role: "member", Kind: "app", Scopes: []string{"messages:write", "messages:read"}}, warn(), chat.ErrForbidden},
		{"not a member", other, warn(), chat.ErrNotFound},
		{"unknown channel", e.bot, warn(func(in *protocol.AlertInput) { in.Channel = "nowhere" }), chat.ErrNotFound},
		{"channel not joined", e.bot, warn(func(in *protocol.AlertInput) { in.Channel = "secret" }), chat.ErrNotFound},
		{"channel by id not joined", e.bot, warn(func(in *protocol.AlertInput) { in.Channel = secret.ID }), chat.ErrNotFound},
		{"allowlist excludes the channel", withAllowlist(e.bot, secret.ID), warn(), chat.ErrNotFound},
		{"empty allowlist", withAllowlist(e.bot), warn(), chat.ErrNotFound},
	}
	for _, tc := range tests {
		_, err := e.svc.Post(e.ctx, tc.actor, tc.in)
		if !errors.Is(err, tc.target) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.target)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'alert'`); n != 0 {
		t.Errorf("refused posts created %d messages", n)
	}

	// Channel by name (with # and any case), by id, and through an allowlist that admits it.
	for _, ref := range []string{"ops", "#ops", "#OPS", e.ops.ID} {
		res := e.post(withAllowlist(e.bot, e.ops.ID), warn(func(in *protocol.AlertInput) { in.Channel = ref; in.DedupeKey = "k-" + ref }))
		if res.Message.ChannelID != e.ops.ID {
			t.Errorf("channel %q -> %s", ref, res.Message.ChannelID)
		}
	}

	// A disabled app cannot post even though its token scopes are intact.
	yes := true
	if _, err := e.apps.UpdateApp(e.ctx, e.aud(e.admin), e.botApp.ID, updateDisabled(&yes)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Post(e.ctx, e.bot, warn()); !errors.Is(err, chat.ErrForbidden) {
		t.Errorf("disabled app: %v", err)
	}
}

func TestAlertsPostToArchivedChannelIsRefused(t *testing.T) {
	e := newEnv(t)
	if _, err := e.chat.ArchiveChannel(e.ctx, e.admin, e.ops.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Post(e.ctx, e.bot, warn()); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("archived channel: %v", err)
	}
}

func TestAlertsDedupeKeyIsPerApp(t *testing.T) {
	e := newEnv(t)
	_, other := e.newApp("other", e.ops.ID)
	a := e.post(e.bot, warn())
	b := e.post(other, warn())
	if a.Message.ID == b.Message.ID || !b.Created() {
		t.Errorf("two apps share an alert: %s / %s", a.Message.ID, b.Message.ID)
	}
	// One app's key cannot resolve the other's alert.
	if _, err := e.svc.ResolveByKey(e.ctx, other, e.aud(other), "disk", ""); err != nil {
		t.Fatal(err)
	}
	if st := e.alertMessage(a.Message.ID).Alert.State; st != protocol.AlertOpen {
		t.Errorf("resolve by the other app changed the first alert to %s", st)
	}
}

func TestAlertsAppsCannotAckOrResolveByMessageID(t *testing.T) {
	e := newEnv(t)
	_, other := e.newApp("other", e.ops.ID)
	mine := e.post(e.bot, warn())
	// Apps are refused on the message-id operations, even for their own alert
	// and even when they are members of its channel.
	for _, c := range []struct {
		name  string
		actor chat.Actor
	}{{"other app", other}, {"owner app", e.bot}} {
		if _, err := e.svc.Ack(e.ctx, c.actor, e.aud(c.actor), mine.Message.ID); !errors.Is(err, chat.ErrForbidden) {
			t.Errorf("%s ack: %v, want forbidden", c.name, err)
		}
		if _, err := e.svc.Resolve(e.ctx, c.actor, e.aud(c.actor), mine.Message.ID, "by id"); !errors.Is(err, chat.ErrForbidden) {
			t.Errorf("%s resolve: %v, want forbidden", c.name, err)
		}
	}
	if st := e.alertMessage(mine.Message.ID).Alert.State; st != protocol.AlertOpen {
		t.Fatalf("refused app operations changed the alert to %s", st)
	}
	// The other app has no "disk" alert of its own, so its key lookup cannot
	// reach the owner's alert.
	if _, err := e.svc.ResolveByKey(e.ctx, other, e.aud(other), "disk", ""); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("other app resolve by key: %v, want not found", err)
	}
	if st := e.alertMessage(mine.Message.ID).Alert.State; st != protocol.AlertOpen {
		t.Errorf("other app resolved the alert: %s", st)
	}
	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", ""); err != nil {
		t.Fatal(err)
	}
	if st := e.alertMessage(mine.Message.ID).Alert.State; st != protocol.AlertResolved {
		t.Errorf("owner resolve by key left the alert %s", st)
	}
}

func TestAlertsSameKeyInAnotherChannelConflicts(t *testing.T) {
	e := newEnv(t)
	e.channel("ops3", e.bot.ID)
	e.post(e.bot, warn())
	_, err := e.svc.Post(e.ctx, e.bot, warn(func(in *protocol.AlertInput) { in.Channel = "ops3" }))
	if !errors.Is(err, chat.ErrConflict) {
		t.Errorf("same open key in another channel: %v", err)
	}
}

func TestAlertsRepeatRerendersTheBody(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	if !strings.Contains(first.Message.Body, "92% used") {
		t.Fatalf("body = %q", first.Message.Body)
	}
	e.post(e.bot, warn(func(in *protocol.AlertInput) {
		in.Text = "97% used"
		in.Fields = []protocol.AlertField{{Name: "Host", Value: "nas"}}
		l := "https://nas.local/disk"
		in.Link = &l
	}))
	msg := e.alertMessage(first.Message.ID)
	if !strings.Contains(msg.Body, "97% used") || strings.Contains(msg.Body, "92%") || !strings.Contains(msg.Body, "| Host | nas |") || !strings.Contains(msg.Body, "https://nas.local/disk") {
		t.Errorf("body not re-rendered: %q", msg.Body)
	}
	if msg.EditedAt != nil {
		t.Errorf("a re-render marked the message edited: %v", *msg.EditedAt)
	}
	if a := msg.Alert; len(a.Fields) != 1 || a.Link == nil || *a.Link != "https://nas.local/disk" || a.Occurrences != 2 {
		t.Errorf("alert = %+v", a)
	}
	// message.updated carries the new body and the new occurrence count.
	evs := e.events("message.updated")
	if len(evs) != 1 {
		t.Fatalf("message.updated events = %d", len(evs))
	}
	var m protocol.Message
	if err := jsonUnmarshal(evs[0].Payload, &m); err != nil || !strings.Contains(m.Body, "97% used") || m.Alert.Occurrences != 2 {
		t.Errorf("message.updated payload = %+v, %v", m, err)
	}
	// The plain text feeding search follows the new body.
	var plain string
	if err := e.db.Read().QueryRow(`SELECT body_plain FROM messages WHERE id = ?`, first.Message.ID).Scan(&plain); err != nil || !strings.Contains(plain, "97%") {
		t.Errorf("body_plain = %q, %v", plain, err)
	}
}

func TestAlertsDeletedAlertMessageDoesNotBlockItsKey(t *testing.T) {
	e := newEnv(t)
	first := e.post(e.bot, warn())
	if err := e.chat.DeleteMessage(e.ctx, e.admin, first.Message.ID); err != nil {
		t.Fatal(err)
	}
	second := e.post(e.bot, warn())
	if !second.Created() || second.Message.ID == first.Message.ID {
		t.Errorf("post after delete = %s %s", second.Transition, second.Message.ID)
	}
	if _, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), first.Message.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("ack of a deleted alert: %v", err)
	}
}

func TestAlertsOccurredAtIsClampedToNow(t *testing.T) {
	e := newEnv(t)
	past := e.clock.Now().Add(-time.Hour).Format(time.RFC3339)
	future := e.clock.Now().Add(24 * time.Hour).Format(time.RFC3339)
	res := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.OccurredAt = &past }))
	if got := res.Message.Alert.FirstSeenAt; got != protocol.FormatTime(e.clock.Now().Add(-time.Hour).UnixMilli()) {
		t.Errorf("first_seen_at = %s", got)
	}
	res = e.post(e.bot, warn(func(in *protocol.AlertInput) { in.OccurredAt = &future }))
	if got := res.Message.Alert.LastSeenAt; got != protocol.FormatTime(e.clock.Now().UnixMilli()) {
		t.Errorf("last_seen_at = %s, want now", got)
	}
}

func TestAlertsPostValidation(t *testing.T) {
	e := newEnv(t)
	long := func(n int) string { return strings.Repeat("x", n) }
	fields := func(n int) []protocol.AlertField {
		out := make([]protocol.AlertField, n)
		for i := range out {
			out[i] = protocol.AlertField{Name: fmt.Sprintf("f%d", i), Value: "v"}
		}
		return out
	}
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	tests := []struct {
		name  string
		mod   func(*protocol.AlertInput)
		field string
	}{
		{"no channel", func(in *protocol.AlertInput) { in.Channel = "" }, "channel"},
		{"bad severity", func(in *protocol.AlertInput) { in.Severity = "fatal" }, "severity"},
		{"no severity", func(in *protocol.AlertInput) { in.Severity = "" }, "severity"},
		{"blank title", func(in *protocol.AlertInput) { in.Title = " \n " }, "title"},
		{"long title", func(in *protocol.AlertInput) { in.Title = long(201) }, "title"},
		{"long text", func(in *protocol.AlertInput) { in.Text = long(4001) }, "text"},
		{"no key", func(in *protocol.AlertInput) { in.DedupeKey = "" }, "dedupe_key"},
		{"long key", func(in *protocol.AlertInput) { in.DedupeKey = long(201) }, "dedupe_key"},
		{"21 fields", func(in *protocol.AlertInput) { in.Fields = fields(21) }, "fields"},
		{"empty field name", func(in *protocol.AlertInput) { in.Fields = []protocol.AlertField{{Name: " ", Value: "v"}} }, "fields[0]"},
		{"long field name", func(in *protocol.AlertInput) { in.Fields = []protocol.AlertField{{Name: long(65), Value: "v"}} }, "fields[0]"},
		{"long field value", func(in *protocol.AlertInput) { in.Fields = []protocol.AlertField{{Name: "n", Value: long(513)}} }, "fields[0]"},
		{"ftp link", func(in *protocol.AlertInput) { in.Link = str("ftp://nas/x") }, "link"},
		{"javascript link", func(in *protocol.AlertInput) { in.Link = str("javascript:alert(1)") }, "link"},
		{"relative link", func(in *protocol.AlertInput) { in.Link = str("/disk") }, "link"},
		{"no host", func(in *protocol.AlertInput) { in.Link = str("https:///x") }, "link"},
		{"link with blank", func(in *protocol.AlertInput) { in.Link = str("https://nas/a b") }, "link"},
		{"link with credentials", func(in *protocol.AlertInput) { in.Link = str("https://u:p@nas/") }, "link"},
		{"link with angle bracket", func(in *protocol.AlertInput) { in.Link = str("https://nas/<x>") }, "link"},
		{"empty link", func(in *protocol.AlertInput) { in.Link = str("") }, "link"},
		{"long link", func(in *protocol.AlertInput) { in.Link = str("https://nas/" + long(2100)) }, "link"},
		{"renotify too short", func(in *protocol.AlertInput) { in.RenotifyAfterS = num(5) }, "renotify_after_s"},
		{"renotify too long", func(in *protocol.AlertInput) { in.RenotifyAfterS = num(86401) }, "renotify_after_s"},
		{"bad time", func(in *protocol.AlertInput) { in.OccurredAt = str("yesterday") }, "occurred_at"},
	}
	for _, tc := range tests {
		_, err := e.svc.Post(e.ctx, e.bot, warn(tc.mod))
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields[tc.field] == "" {
			t.Errorf("%s: err = %v, want validation error on %s", tc.name, err, tc.field)
		}
	}
	// The limits themselves are accepted.
	ok := warn(func(in *protocol.AlertInput) {
		in.Title = long(200)
		in.Text = long(4000)
		in.DedupeKey = long(200)
		in.Fields = fields(20)
		in.Link = str("http://nas.local/x?y=(1)")
		in.RenotifyAfterS = num(10)
	})
	res, err := e.svc.Post(e.ctx, e.bot, ok)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if got := len([]rune(res.Message.Body)); got > chat.MaxBodyChars {
		t.Errorf("body has %d characters", got)
	}
}

func TestAlertsRender(t *testing.T) {
	body := Render("Disk *almost* [full]", "92% used\non nas", []protocol.AlertField{
		{Name: "Host", Value: "nas | primary"}, {Name: "Mount", Value: "/data\nvolume"},
	}, "https://nas.local/disk?a=(1)")
	want := "**Disk \\*almost\\* \\[full\\]**\n\n92% used\non nas\n\n| Field | Value |\n| --- | --- |\n" +
		"| Host | nas \\| primary |\n| Mount | /data volume |\n\n[Open link](<https://nas.local/disk?a=(1)>)"
	if body != want {
		t.Errorf("body:\n%s\nwant:\n%s", body, want)
	}
	if got := Render("t", "", nil, ""); got != "**t**" {
		t.Errorf("title only = %q", got)
	}
	// Markup in the title is neutralised: no raw HTML, no link or emphasis injection.
	got := Render("<img src=x onerror=alert(1)> `x` _y_ ~z~", "", nil, "")
	if strings.ContainsAny(strings.ReplaceAll(got, `\<`, ""), "<") || strings.Contains(got, "`x`") {
		t.Errorf("title not escaped: %q", got)
	}

	// Oversized parts are cut to fit a message: trailing fields first, then the text.
	huge := strings.Repeat("é", 4000)
	fields := make([]protocol.AlertField, 20)
	for i := range fields {
		fields[i] = protocol.AlertField{Name: strings.Repeat("n", 64), Value: strings.Repeat("v", 512)}
	}
	full := Render(strings.Repeat("t", 200), huge, fields, "https://nas.local/"+strings.Repeat("p", 1900))
	if n := len([]rune(full)); n > chat.MaxBodyChars || n < chat.MaxBodyChars-700 {
		t.Errorf("fitted body has %d characters", n)
	}
	if !strings.HasPrefix(full, "**"+strings.Repeat("t", 200)+"**") || !strings.HasSuffix(full, ">)") || !strings.Contains(full, huge) {
		t.Error("title, text or link lost while fitting")
	}
	// With no room for fields the text is cut and marked.
	cut := Render("t", strings.Repeat("é", 9000), nil, "")
	if n := len([]rune(cut)); n != chat.MaxBodyChars || !strings.HasSuffix(cut, "…") {
		t.Errorf("cut text: %d characters, ends %q", n, cut[len(cut)-4:])
	}
}

func TestAlertsListIsMembershipFiltered(t *testing.T) {
	e := newEnv(t)
	secret := e.channel("secret", e.bot.ID)
	bob := e.human("bob", protocol.RoleMember)
	reader := withScopes(e.bot, protocol.ScopeMessagesRead)
	a := e.post(e.bot, warn())
	b := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.DedupeKey = "cpu"; in.Severity = protocol.SeverityCritical }))
	c := e.post(e.bot, warn(func(in *protocol.AlertInput) { in.Channel = "secret"; in.DedupeKey = "hidden" }))
	if _, err := e.svc.Ack(e.ctx, e.alice, e.aud(e.alice), b.Message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ResolveByKey(e.ctx, e.bot, e.aud(e.bot), "disk", ""); err != nil {
		t.Fatal(err)
	}

	ids := func(p protocol.AlertPage) []string {
		out := make([]string, len(p.Items))
		for i, m := range p.Items {
			out[i] = m.ID
		}
		return out
	}
	list := func(actor chat.Actor, p ListParams) protocol.AlertPage {
		t.Helper()
		page, err := e.svc.List(e.ctx, actor, p)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	// Alice is in #ops only: the alert in #secret is invisible to her, newest first.
	if got := ids(list(e.alice, ListParams{})); !slices.Equal(got, []string{b.Message.ID, a.Message.ID}) {
		t.Errorf("alice sees %v", got)
	}
	if got := ids(list(bob, ListParams{})); len(got) != 0 {
		t.Errorf("non-member sees %v", got)
	}
	if got := ids(list(e.human("root2", protocol.RoleAdmin), ListParams{})); len(got) != 0 {
		t.Errorf("an admin who is not a member sees %v", got)
	}
	if got := ids(list(reader, ListParams{})); !slices.Equal(got, []string{c.Message.ID, b.Message.ID, a.Message.ID}) {
		t.Errorf("the bot sees %v", got)
	}
	for state, want := range map[string][]string{
		protocol.AlertOpen:     nil,
		protocol.AlertAcked:    {b.Message.ID},
		protocol.AlertResolved: {a.Message.ID},
	} {
		if got := ids(list(e.alice, ListParams{State: state})); !slices.Equal(got, want) {
			t.Errorf("state %s: %v, want %v", state, got, want)
		}
	}
	if got := ids(list(reader, ListParams{ChannelID: secret.ID})); !slices.Equal(got, []string{c.Message.ID}) {
		t.Errorf("channel filter: %v", got)
	}
	if got := ids(list(e.alice, ListParams{ChannelID: secret.ID})); len(got) != 0 {
		t.Errorf("channel filter on an invisible channel: %v", got)
	}
	// Token allowlist and scopes bind apps.
	if got := ids(list(withAllowlist(reader, e.ops.ID), ListParams{})); !slices.Equal(got, []string{b.Message.ID, a.Message.ID}) {
		t.Errorf("allowlisted bot sees %v", got)
	}
	if got := ids(list(withAllowlist(reader), ListParams{})); len(got) != 0 {
		t.Errorf("bot with an empty allowlist sees %v", got)
	}
	if _, err := e.svc.List(e.ctx, chat.Actor{ID: e.bot.ID, Role: "member", Kind: "app", Scopes: []string{"files:write"}}, ListParams{}); !errors.Is(err, chat.ErrForbidden) {
		t.Errorf("list without scope: %v", err)
	}
	// The page carries message bodies: alerts:write alone and channels:read alone are refused.
	for _, scopes := range [][]string{{protocol.ScopeAlertsWrite}, {protocol.ScopeChannelsRead}} {
		if _, err := e.svc.List(e.ctx, withScopes(e.bot, scopes...), ListParams{}); !errors.Is(err, chat.ErrForbidden) {
			t.Errorf("list with %v: %v, want forbidden", scopes, err)
		}
	}

	// Pages: limit and cursor.
	p1 := list(reader, ListParams{Limit: 2})
	if len(p1.Items) != 2 || p1.NextCursor == nil || *p1.NextCursor != b.Message.ID {
		t.Fatalf("page 1 = %v cursor %v", ids(p1), p1.NextCursor)
	}
	p2 := list(reader, ListParams{Limit: 2, Cursor: *p1.NextCursor})
	if !slices.Equal(ids(p2), []string{a.Message.ID}) || p2.NextCursor != nil {
		t.Errorf("page 2 = %v cursor %v", ids(p2), p2.NextCursor)
	}
	for _, bad := range []ListParams{{State: "closed"}, {Cursor: strings.Repeat("c", 65)}} {
		var ve *chat.ValidationError
		if _, err := e.svc.List(e.ctx, e.alice, bad); !errors.As(err, &ve) {
			t.Errorf("list %+v: %v", bad, err)
		}
	}
	if page := list(e.alice, ListParams{ChannelID: "nope"}); page.Items == nil || len(page.Items) != 0 {
		t.Errorf("empty page items = %#v", page.Items)
	}
}

func TestAlertsTransitionRules(t *testing.T) {
	now := int64(10_000_000)
	mk := func(sev, state string, notified, every int64) row {
		r := row{Severity: sev, State: state}
		if notified >= 0 {
			r.LastNotified.Valid, r.LastNotified.Int64 = true, notified
		}
		if every >= 0 {
			r.RenotifyS.Valid, r.RenotifyS.Int64 = true, every
		}
		return r
	}
	tests := []struct {
		name string
		r    row
		sev  string
		want string
	}{
		{"escalation", mk("warning", "open", now, 60), "critical", "escalated"},
		{"escalation beats renotify", mk("info", "open", 0, 60), "warning", "escalated"},
		{"escalation of an acked alert", mk("warning", "acked", now, 60), "critical", "escalated"},
		{"same severity inside interval", mk("warning", "open", now-59_000, 60), "warning", "updated_other"},
		{"renotify at the interval", mk("warning", "open", now-60_000, 60), "warning", "renotify_due"},
		{"lower severity, renotify due", mk("critical", "open", 0, 60), "info", "renotify_due"},
		{"acked never renotifies", mk("warning", "acked", 0, 60), "warning", "updated_other"},
		{"renotify off", mk("warning", "open", 0, 0), "warning", "updated_other"},
		{"default interval", mk("warning", "open", now-1_800_000, -1), "warning", "renotify_due"},
		{"default interval not yet", mk("warning", "open", now-1_799_999, -1), "warning", "updated_other"},
		{"never notified", mk("warning", "open", -1, 60), "warning", "renotify_due"},
	}
	for _, tc := range tests {
		if got := transition(tc.r, tc.sev, now, 1800); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
