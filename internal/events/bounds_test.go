package events

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestMembershipCacheDoesNotMemoizeUnknownChannels(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	c := NewMembershipCache(context.Background(), w.db.Read(), nil)
	for i := range 100 {
		id := fmt.Sprintf("ghost-%d", i)
		if c.IsMember(id, "alice") || c.ChannelKind(id) != "" || len(c.Members(id)) != 0 {
			t.Fatalf("%s reported as known", id)
		}
	}
	if ch, us := c.size(); ch != 0 || us != 0 {
		t.Fatalf("cache holds %d channels and %d users; unknown channels must not be memoized", ch, us)
	}
	// A channel created after a miss is seen at once: there is no stale
	// negative entry to invalidate.
	w.channel(t, "ghost-1", protocol.ChannelPublic, "alice")
	if !c.IsMember("ghost-1", "alice") {
		t.Fatal("channel created after a miss is not visible")
	}
}

func TestMembershipCacheEvictsLeastRecentlyUsed(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	for _, id := range []string{"a", "b", "c"} {
		w.channel(t, id, protocol.ChannelPublic, "alice")
	}
	c := NewMembershipCache(context.Background(), w.db.Read(), nil)
	c.channels.max = 2
	c.IsMember("a", "alice")
	c.IsMember("b", "alice")
	c.IsMember("a", "alice") // a becomes the most recently used
	c.IsMember("c", "alice") // evicts b
	if ch, _ := c.size(); ch != 2 {
		t.Fatalf("cached channels = %d, want 2", ch)
	}
	c.mu.Lock()
	_, hasA := c.channels.items["a"]
	_, hasB := c.channels.items["b"]
	c.mu.Unlock()
	if !hasA || hasB {
		t.Fatalf("a cached=%v b cached=%v; want the least recently used b evicted", hasA, hasB)
	}
	if !c.IsMember("b", "alice") {
		t.Fatal("evicted channel did not reload")
	}
}

func TestTypingRandomChannelsStayBounded(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.user(t, "gus", protocol.RoleGuest)
	w.channel(t, "pub", protocol.ChannelPublic, "alice", "gus")
	alice := w.subscribe("alice", protocol.RoleMember, "")
	gus := w.subscribe("gus", protocol.RoleGuest, "")

	rng := rand.New(rand.NewPCG(1, 2))
	for range 50000 {
		alice.Typing(fmt.Sprintf("%016x%016x", rng.Uint64(), rng.Uint64()))
	}
	if ch, us := w.hub.Membership().size(); ch > maxCachedChannels || us > maxCachedUsers || ch != 0 {
		t.Fatalf("membership cache holds %d channels and %d users after random typing", ch, us)
	}
	w.hub.mu.Lock()
	n := len(w.hub.typingAt)
	w.hub.mu.Unlock()
	if n != 0 || n > maxTypingEntries {
		t.Fatalf("typing map holds %d records after random typing", n)
	}
	if got := typingFrom(t, w, gus); len(got) != 0 {
		t.Fatalf("random channels relayed typing: %v", got)
	}

	alice.Typing("pub")
	if got := typingFrom(t, w, gus); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("real channel typing = %v, want [alice]", got)
	}
}

func TestTypingThrottleMapPrunesStaleRecords(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.channel(t, "pub", protocol.ChannelPublic, "alice")
	alice := w.subscribe("alice", protocol.RoleMember, "")

	w.hub.mu.Lock()
	for i := range 500 {
		w.hub.typingAt[fmt.Sprintf("old-%d", i)] = w.clock.Now()
	}
	w.hub.mu.Unlock()
	w.clock.Advance(TypingInterval)
	alice.Typing("pub")

	w.hub.mu.Lock()
	n := len(w.hub.typingAt)
	w.hub.mu.Unlock()
	if n != 1 {
		t.Fatalf("typing map holds %d records, want 1 once stale ones are pruned", n)
	}
}

func TestTypingThrottleMapIsCapped(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.user(t, "gus", protocol.RoleGuest)
	w.channel(t, "pub", protocol.ChannelPublic, "alice", "gus")
	alice := w.subscribe("alice", protocol.RoleMember, "")
	gus := w.subscribe("gus", protocol.RoleGuest, "")

	w.hub.mu.Lock()
	for i := range maxTypingEntries {
		w.hub.typingAt[fmt.Sprintf("fresh-%d", i)] = w.clock.Now()
	}
	w.hub.mu.Unlock()
	alice.Typing("pub") // no room and nothing stale: dropped
	if got := typingFrom(t, w, gus); len(got) != 0 {
		t.Fatalf("signal over the cap relayed: %v", got)
	}
	w.hub.mu.Lock()
	n := len(w.hub.typingAt)
	w.hub.mu.Unlock()
	if n != maxTypingEntries {
		t.Fatalf("typing map holds %d records, want the cap %d", n, maxTypingEntries)
	}

	w.clock.Advance(TypingInterval)
	alice.Typing("pub") // the stale fill is pruned, so the signal goes through
	if got := typingFrom(t, w, gus); !slices.Equal(got, []string{"alice"}) {
		t.Fatalf("typing after pruning = %v, want [alice]", got)
	}
}

func TestPresenceFlappingIsCoalesced(t *testing.T) {
	w := newWorld(t)
	bob := w.subscribe("bob", protocol.RoleMember, "")
	alice := w.subscribe("alice", protocol.RoleMember, "")
	// alice's connect publication is the one broadcast of this window.
	if got := presenceOf(t, w, bob, "alice"); !slices.Equal(got, []string{protocol.PresenceOnline}) {
		t.Fatalf("connect: presence %v, want [online]", got)
	}
	for range 200 {
		alice.SetPresence(protocol.PresenceIdle)
		alice.SetPresence(protocol.PresenceActive)
	}
	alice.SetPresence(protocol.PresenceIdle) // the final state inside the window
	if got := presenceOf(t, w, bob, "alice"); len(got) != 0 {
		t.Fatalf("flapping inside the window broadcast %v", got)
	}

	w.clock.Advance(PresenceCoalesce)
	w.hub.SweepPresence()
	if got := presenceOf(t, w, bob, "alice"); !slices.Equal(got, []string{protocol.PresenceIdle}) {
		t.Fatalf("final state after the window: presence %v, want [idle]", got)
	}

	// A change that returns to the published state inside a window is not
	// broadcast at all.
	alice.SetPresence(protocol.PresenceActive)
	alice.SetPresence(protocol.PresenceIdle)
	w.clock.Advance(PresenceCoalesce)
	w.hub.SweepPresence()
	if got := presenceOf(t, w, bob, "alice"); len(got) != 0 {
		t.Fatalf("net-unchanged state broadcast %v", got)
	}
}

func TestPresenceSlotsArePruned(t *testing.T) {
	w := newWorld(t)
	for i := range 50 {
		s := w.subscribe(fmt.Sprintf("u%d", i), protocol.RoleMember, "")
		s.Leave()
	}
	w.clock.Advance(OfflineGrace)
	w.hub.SweepPresence() // everyone turns offline and is stamped now
	w.clock.Advance(PresenceCoalesce)
	w.hub.SweepPresence() // the stamps are stale and dropped
	w.hub.mu.Lock()
	n := len(w.hub.presenceSlots)
	w.hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("presence slots = %d after every user went offline, want 0", n)
	}
}
