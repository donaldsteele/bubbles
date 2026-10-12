package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// typingFrom returns the typists of the typing frames s got before marker.
func typingFrom(t *testing.T, w *world, s *testSub) []string {
	t.Helper()
	marker := w.nextMarker()
	w.hub.Publish(Event{Seq: marker, Type: TypeSettingsUpdated})
	var got []string
	for {
		e := recvFrame(t, s)
		if e.Seq == marker {
			return got
		}
		if e.Type != TypeTyping {
			continue
		}
		var p protocol.TypingEvent
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if e.Seq != 0 || e.ChannelID != p.ChannelID {
			t.Fatalf("typing frame %+v", e)
		}
		got = append(got, p.UserID)
	}
}

func TestTypingRelayAndRateLimit(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.user(t, "bob", protocol.RoleMember)
	w.user(t, "gus", protocol.RoleGuest)
	w.channel(t, "pub", protocol.ChannelPublic, "alice", "gus")
	alice := w.subscribe("alice", protocol.RoleMember, "")
	bob := w.subscribe("bob", protocol.RoleMember, "")
	gus := w.subscribe("gus", protocol.RoleGuest, "")

	alice.Typing("pub")
	alice.Typing("pub") // within 3 s: dropped
	if got := typingFrom(t, w, gus); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("gus got typing %v, want [alice]", got)
	}
	if got := typingFrom(t, w, alice); len(got) != 0 {
		t.Fatalf("typist got own typing %v", got)
	}
	if got := typingFrom(t, w, bob); len(got) != 0 {
		t.Fatalf("non-member got typing %v", got)
	}

	w.clock.Advance(TypingInterval - time.Millisecond)
	alice.Typing("pub")
	if got := typingFrom(t, w, gus); len(got) != 0 {
		t.Fatalf("typing inside interval relayed: %v", got)
	}
	w.clock.Advance(time.Millisecond)
	alice.Typing("pub")
	if got := typingFrom(t, w, gus); len(got) != 1 {
		t.Fatalf("typing after interval: %v", got)
	}

	// A non-member's typing is dropped.
	bob.Typing("pub")
	if got := typingFrom(t, w, gus); len(got) != 0 {
		t.Fatalf("non-member typing relayed: %v", got)
	}
}
