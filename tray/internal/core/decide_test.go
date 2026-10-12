package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type vectorFile struct {
	Version int `json:"version"`
	Vectors []struct {
		Name  string               `json:"name"`
		Input notifyrules.Input    `json:"input"`
		Want  notifyrules.Decision `json:"want"`
	} `json:"vectors"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "notify-rules.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func str(s string) *string { return &s }

// engineFor builds an Engine whose state reproduces one rules input.
func engineFor(in notifyrules.Input) *Engine {
	e := NewEngine()
	mem := protocol.Membership{
		ChannelID: "c1", UserID: in.RecipientID, NotifyLevel: in.NotifyLevel, AlertMinSeverity: in.AlertMinSeverity,
	}
	if in.MutedUntil != nil {
		mem.MutedUntil = str(protocol.FormatTime(*in.MutedUntil))
	}
	e.Load(&protocol.Bootstrap{
		Me:          protocol.Me{User: protocol.User{ID: in.RecipientID, DisplayName: "Me"}},
		Users:       []protocol.User{{ID: in.AuthorID, DisplayName: "Author"}},
		Channels:    []protocol.Channel{{ID: "c1", Kind: in.ChannelKind, Name: str("general")}},
		Memberships: []protocol.Membership{mem},
	})
	return e
}

func eventFor(t *testing.T, in notifyrules.Input) protocol.Event {
	t.Helper()
	m := protocol.Message{
		ID: "m1", ChannelID: "c1", Kind: in.MessageKind, Body: "hello",
		Author:   protocol.Author{ID: in.AuthorID, DisplayName: "Author"},
		Mentions: protocol.Mentions{UserIDs: in.Mentions.UserIDs, Channel: in.Mentions.Channel, Here: in.Mentions.Here},
	}
	if in.MessageKind == notifyrules.KindAlert {
		m.Alert = &protocol.Alert{Severity: in.AlertSeverity, Title: "Disk full"}
		if in.AlertTransition != notifyrules.TransitionCreated {
			return protocol.Event{Seq: 1, Type: "alert.updated", ChannelID: "c1",
				Data: raw(t, protocol.AlertUpdatedEvent{Message: m, Transition: in.AlertTransition})}
		}
	}
	return protocol.Event{Seq: 1, Type: "message.created", ChannelID: "c1", Data: raw(t, m)}
}

// TestDecisionMatchesSharedVectors replays the shared notification vectors
// through the tray engine: the tray must reach the same decision as the
// server and the web app.
func TestDecisionMatchesSharedVectors(t *testing.T) {
	f := loadVectors(t)
	tested := 0
	for _, v := range f.Vectors {
		if !v.Input.RecipientOnline && v.Input.Mentions.Here {
			continue // the tray is always an online client; only @here depends on it
		}
		tested++
		t.Run(v.Name, func(t *testing.T) {
			e := engineFor(v.Input)
			res := e.Handle(eventFor(t, v.Input), time.UnixMilli(v.Input.Now))
			if got := res.Notify != nil; got != v.Want.Notify {
				t.Fatalf("notify = %v, want %v (%s)", got, v.Want.Notify, v.Want.Reason)
			}
			if res.Refresh {
				t.Fatal("asked for a refresh although the state is complete")
			}
		})
	}
	if tested < 70 {
		t.Fatalf("only %d vectors replayed", tested)
	}
}

func TestEngineNotificationText(t *testing.T) {
	in := notifyrules.Input{
		RecipientID: "me", AuthorID: "bob", ChannelKind: notifyrules.ChannelPublic, NotifyLevel: notifyrules.LevelAll,
		AlertMinSeverity: notifyrules.SeverityInfo, MessageKind: notifyrules.KindText,
	}
	e := engineFor(in)
	long := strings.Repeat("word ", 60)
	ev := eventFor(t, in)
	var m protocol.Message
	_ = json.Unmarshal(ev.Data, &m)
	m.Body = "  hi\n  there  " + long
	ev.Data = raw(t, m)
	n := e.Handle(ev, time.Now()).Notify
	if n == nil || n.Title != "Author in #general" || !strings.HasPrefix(n.Body, "hi there word") || len([]rune(n.Body)) != maxBodyRunes || !strings.HasSuffix(n.Body, "...") {
		t.Fatalf("notification = %+v", n)
	}
	if n.ChannelID != "c1" || n.MessageID != "m1" || n.Critical || n.Silent {
		t.Fatalf("notification = %+v", n)
	}

	// A webhook post shows its override name; a DM has no channel suffix.
	m.OverrideName = str("Deploy bot")
	ev.Data = raw(t, m)
	if n := e.Handle(ev, time.Now()).Notify; n == nil || n.Title != "Deploy bot in #general" {
		t.Fatalf("override title = %+v", n)
	}
	dm := in
	dm.ChannelKind = notifyrules.ChannelDM
	e2 := NewEngine()
	e2.Load(&protocol.Bootstrap{
		Me:          protocol.Me{User: protocol.User{ID: "me"}},
		Users:       []protocol.User{{ID: "bob", DisplayName: "Bob"}},
		Channels:    []protocol.Channel{{ID: "c1", Kind: dm.ChannelKind}},
		Memberships: []protocol.Membership{{ChannelID: "c1", UserID: "me", NotifyLevel: "all", AlertMinSeverity: "info"}},
	})
	m.OverrideName = nil
	ev.Data = raw(t, m)
	if n := e2.Handle(ev, time.Now()).Notify; n == nil || n.Title != "Bob" {
		t.Fatalf("dm title = %+v", n)
	}
}

func alertEvent(t *testing.T, id, sev, transition string) protocol.Event {
	t.Helper()
	m := protocol.Message{
		ID: id, ChannelID: "c1", Kind: notifyrules.KindAlert, Author: protocol.Author{ID: "bot"},
		Alert: &protocol.Alert{Severity: sev, Title: "Disk full"},
	}
	if transition == protocol.AlertTransitionCreated {
		return protocol.Event{Seq: 1, Type: "message.created", ChannelID: "c1", Data: raw(t, m)}
	}
	return protocol.Event{Seq: 1, Type: "alert.updated", ChannelID: "c1", Data: raw(t, protocol.AlertUpdatedEvent{Message: m, Transition: transition})}
}

func TestIndicatorStates(t *testing.T) {
	in := notifyrules.Input{RecipientID: "me", AuthorID: "bob", ChannelKind: notifyrules.ChannelPublic, NotifyLevel: notifyrules.LevelAll, AlertMinSeverity: notifyrules.SeverityInfo, MessageKind: notifyrules.KindText}
	e := engineFor(in)
	now := time.Now()
	if e.Indicator() != IndicatorNormal {
		t.Fatal("fresh engine is not normal")
	}
	e.Handle(eventFor(t, in), now)
	if e.Indicator() != IndicatorUnread {
		t.Fatalf("after a message: %v", e.Indicator())
	}
	// Reading the channel elsewhere clears the unread mark.
	if r := e.Handle(protocol.Event{Type: "read.updated", Data: raw(t, protocol.ReadEvent{ChannelID: "c1", Unread: 0})}, now); !r.Changed {
		t.Fatal("read.updated did not change the indicator")
	}
	if e.Indicator() != IndicatorNormal {
		t.Fatalf("after read: %v", e.Indicator())
	}

	// A critical alert turns the icon critical until it is acknowledged.
	res := e.Handle(alertEvent(t, "a1", notifyrules.SeverityCritical, protocol.AlertTransitionCreated), now)
	if res.Notify == nil || !res.Notify.Critical || e.Indicator() != IndicatorCritical {
		t.Fatalf("critical alert: %+v, indicator %v", res.Notify, e.Indicator())
	}
	e.Handle(in2(t, in), now) // plain unread message on top
	if e.Indicator() != IndicatorCritical {
		t.Fatal("critical must win over unread")
	}
	if r := e.Handle(alertEvent(t, "a1", notifyrules.SeverityCritical, protocol.AlertTransitionAcked), now); r.Notify != nil || !r.Changed {
		t.Fatalf("ack: %+v", r)
	}
	if e.Indicator() != IndicatorUnread {
		t.Fatalf("after ack: %v", e.Indicator())
	}
	e.MarkSeen()
	if e.Indicator() != IndicatorNormal {
		t.Fatalf("after MarkSeen: %v", e.Indicator())
	}
	// A warning alert notifies but is not critical.
	res = e.Handle(alertEvent(t, "a2", notifyrules.SeverityWarning, protocol.AlertTransitionCreated), now)
	if res.Notify == nil || res.Notify.Critical || e.Indicator() != IndicatorUnread {
		t.Fatalf("warning alert: %+v", res.Notify)
	}
}

func in2(t *testing.T, in notifyrules.Input) protocol.Event {
	t.Helper()
	ev := eventFor(t, in)
	var m protocol.Message
	_ = json.Unmarshal(ev.Data, &m)
	m.ID = "m2"
	ev.Data = raw(t, m)
	return ev
}

func TestPauseIsSilentButIndicatorsStay(t *testing.T) {
	in := notifyrules.Input{RecipientID: "me", AuthorID: "bob", ChannelKind: notifyrules.ChannelPublic, NotifyLevel: notifyrules.LevelAll, AlertMinSeverity: notifyrules.SeverityInfo, MessageKind: notifyrules.KindText}
	e := engineFor(in)
	now := time.Now()
	e.SetPausedUntil(now.Add(time.Hour).UnixMilli())
	res := e.Handle(eventFor(t, in), now)
	if res.Notify == nil || !res.Notify.Silent || e.Indicator() != IndicatorUnread {
		t.Fatalf("paused: %+v, indicator %v", res.Notify, e.Indicator())
	}
	res = e.Handle(in2(t, in), now.Add(2*time.Hour))
	if res.Notify == nil || res.Notify.Silent {
		t.Fatalf("after the pause: %+v", res.Notify)
	}
}

func TestEngineRefreshAndMembershipUpdates(t *testing.T) {
	in := notifyrules.Input{RecipientID: "me", AuthorID: "bob", ChannelKind: notifyrules.ChannelPublic, NotifyLevel: notifyrules.LevelAll, AlertMinSeverity: notifyrules.SeverityInfo, MessageKind: notifyrules.KindText}
	e := engineFor(in)
	now := time.Now()

	// A channel the engine does not know asks for a new bootstrap.
	ev := eventFor(t, in)
	var m protocol.Message
	_ = json.Unmarshal(ev.Data, &m)
	m.ChannelID = "unknown"
	ev.Data = raw(t, m)
	if r := e.Handle(ev, now); !r.Refresh || r.Notify != nil {
		t.Fatalf("unknown channel: %+v", r)
	}
	for _, typ := range []string{"sdk.resync_required", "sdk.instance_changed", "channel.created"} {
		if r := e.Handle(protocol.Event{Type: typ}, now); !r.Refresh {
			t.Errorf("%s did not ask for a refresh", typ)
		}
	}

	// Muting the channel through membership.updated silences it.
	mem := protocol.Membership{ChannelID: "c1", UserID: "me", NotifyLevel: "none", AlertMinSeverity: "info"}
	e.Handle(protocol.Event{Type: "membership.updated", Data: raw(t, mem)}, now)
	if r := e.Handle(eventFor(t, in), now); r.Notify != nil {
		t.Fatal("notify level none still notifies")
	}
	// Someone else's membership is ignored.
	mem.UserID, mem.NotifyLevel = "other", "all"
	e.Handle(protocol.Event{Type: "membership.updated", Data: raw(t, mem)}, now)
	if r := e.Handle(eventFor(t, in), now); r.Notify != nil {
		t.Fatal("another user's membership changed my level")
	}
	// Garbage payloads and deleted messages are ignored.
	if r := e.Handle(protocol.Event{Type: "message.created", Data: json.RawMessage(`[1]`)}, now); r != (Result{}) {
		t.Fatalf("garbage: %+v", r)
	}
}
