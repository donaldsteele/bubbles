package events

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// presenceOf collects the presence states of user seen by s before marker.
func presenceOf(t *testing.T, w *world, s *testSub, user string) []string {
	t.Helper()
	marker := w.nextMarker()
	w.hub.Publish(Event{Seq: marker, Type: TypeSettingsUpdated})
	var got []string
	for {
		e := recvFrame(t, s)
		if e.Seq == marker {
			return got
		}
		if e.Type != TypePresenceChanged {
			continue
		}
		var p protocol.PresenceEvent
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if e.Seq != 0 {
			t.Fatalf("presence frame has seq %d, want 0", e.Seq)
		}
		if p.UserID == user {
			got = append(got, p.State)
		}
	}
}

func TestPresenceOnlineIdleOffline(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.channel(t, "pub", protocol.ChannelPublic, "alice")
	bob := w.subscribe("bob", protocol.RoleMember, "")
	alice := w.subscribe("alice", protocol.RoleMember, "")
	check := func(step string, want ...string) {
		t.Helper()
		if got := presenceOf(t, w, bob, "alice"); !slices.Equal(got, want) {
			t.Fatalf("%s: alice presence %v, want %v", step, got, want)
		}
	}
	check("connect", protocol.PresenceOnline)

	w.clock.Advance(ActivityWindow - time.Second)
	w.hub.SweepPresence()
	check("before activity window")
	w.clock.Advance(time.Second)
	w.hub.SweepPresence()
	check("activity window over", protocol.PresenceIdle)

	// Each change comes after the coalescing gap, so it is published at once.
	w.clock.Advance(PresenceCoalesce)
	alice.SetPresence(protocol.PresenceActive)
	check("active report", protocol.PresenceOnline)
	w.clock.Advance(PresenceCoalesce)
	alice.SetPresence(protocol.PresenceIdle)
	check("idle report", protocol.PresenceIdle)
	w.clock.Advance(PresenceCoalesce)
	alice.Typing("pub") // typing counts as activity
	check("typing", protocol.PresenceOnline)

	// Reconnect inside the grace period: no flapping.
	alice.Leave()
	w.clock.Advance(OfflineGrace - time.Second)
	w.hub.SweepPresence()
	alice = w.subscribe("alice", protocol.RoleMember, "")
	check("reconnect within grace")

	alice.Leave()
	w.clock.Advance(OfflineGrace - time.Second)
	w.hub.SweepPresence()
	check("inside grace")
	w.clock.Advance(time.Second)
	w.hub.SweepPresence()
	check("grace over", protocol.PresenceOffline)
	w.hub.SweepPresence()
	check("stays offline")
}

func TestPresenceVisibilityAndSnapshot(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.user(t, "bob", protocol.RoleMember)
	w.user(t, "gus", protocol.RoleGuest)
	w.channel(t, "pub", protocol.ChannelPublic, "alice", "gus")
	gus := w.subscribe("gus", protocol.RoleGuest, "")
	w.subscribe("alice", protocol.RoleMember, "")
	w.subscribe("bob", protocol.RoleMember, "")
	if got := presenceOf(t, w, gus, "alice"); !slices.Equal(got, []string{protocol.PresenceOnline}) {
		t.Fatalf("guest sees alice %v", got)
	}
	if got := presenceOf(t, w, gus, "bob"); len(got) != 0 {
		t.Fatalf("guest must not see bob's presence, got %v", got)
	}

	var users []string
	for _, f := range w.hub.PresenceSnapshot(Viewer{UserID: "gus", Role: protocol.RoleGuest}) {
		var e protocol.Event
		var p protocol.PresenceEvent
		_ = json.Unmarshal(f.Data, &e)
		_ = json.Unmarshal(e.Data, &p)
		users = append(users, p.UserID+"="+p.State)
	}
	if want := []string{"alice=online", "gus=online"}; !slices.Equal(users, want) {
		t.Fatalf("guest snapshot %v, want %v", users, want)
	}
	if n := len(w.hub.PresenceSnapshot(Viewer{UserID: "bob", Role: protocol.RoleMember})); n != 3 {
		t.Fatalf("member snapshot has %d users, want 3", n)
	}
}

func TestUserFocusedRecently(t *testing.T) {
	w := newWorld(t)
	s := w.subscribe("alice", protocol.RoleMember, "")
	if w.hub.UserFocusedRecently("alice", time.Minute) {
		t.Fatal("no focus reported yet")
	}
	s.Focus(true)
	w.clock.Advance(10 * time.Minute)
	if !w.hub.UserFocusedRecently("alice", time.Minute) {
		t.Fatal("a visible connection counts as focused")
	}
	s.Focus(false)
	w.clock.Advance(59 * time.Second)
	if !w.hub.UserFocusedRecently("alice", time.Minute) {
		t.Fatal("hidden 59s ago is within 60s")
	}
	w.clock.Advance(2 * time.Second)
	if w.hub.UserFocusedRecently("alice", time.Minute) {
		t.Fatal("hidden 61s ago is not recent")
	}
	s.Focus(true)
	s.Leave()
	w.clock.Advance(30 * time.Second)
	if !w.hub.UserFocusedRecently("alice", time.Minute) {
		t.Fatal("a visible connection closed 30s ago is recent")
	}
	if w.hub.UserFocusedRecently("bob", time.Minute) {
		t.Fatal("unknown user is not focused")
	}
}
