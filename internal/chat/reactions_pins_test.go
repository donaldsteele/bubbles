package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestReactions(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	outsider := e.human("outsider", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)
	msg := e.post(alice, ch.ID, "react to me")

	before := e.head()
	rs, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, "👍")
	if err != nil || len(rs) != 1 || rs[0].Emoji != "👍" || rs[0].Count != 1 {
		t.Fatalf("AddReaction: %+v, %v", rs, err)
	}
	evs := e.events(before)
	wantTypes(t, evs, "reaction.added")
	if evs[0].Channel != ch.ID || !strings.Contains(evs[0].Payload, `"count":1`) {
		t.Fatalf("event = %+v", evs[0])
	}

	// Same user, same emoji: idempotent, no event.
	before = e.head()
	if rs, err = e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, "👍"); err != nil || rs[0].Count != 1 {
		t.Fatalf("repeat: %+v, %v", rs, err)
	}
	wantTypes(t, e.events(before))

	rs, err = e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "👍")
	if err != nil || rs[0].Count != 2 || len(rs[0].UserIDs) != 2 {
		t.Fatalf("second user: %+v, %v", rs, err)
	}
	if _, err = e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "🎉"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.svc.GetMessage(e.ctx, alice.Actor, msg.ID)
	if len(got.Reactions) != 2 || got.Reactions[0].Emoji != "👍" || got.Reactions[1].Emoji != "🎉" {
		t.Fatalf("reactions keep first-use order: %+v", got.Reactions)
	}

	before = e.head()
	rs, err = e.svc.RemoveReaction(e.ctx, alice.Actor, msg.ID, "👍")
	if err != nil || rs[0].Count != 1 {
		t.Fatalf("RemoveReaction: %+v, %v", rs, err)
	}
	evs = e.events(before)
	wantTypes(t, evs, "reaction.removed")
	if !strings.Contains(evs[0].Payload, `"count":1`) {
		t.Fatalf("payload = %s", evs[0].Payload)
	}
	before = e.head()
	if _, err = e.svc.RemoveReaction(e.ctx, alice.Actor, msg.ID, "👍"); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))

	// Validation.
	for name, emoji := range map[string]string{
		"empty": "", "space": "a b", "newline": "👍\n", "tab": "\t", "too long": strings.Repeat("x", MaxEmojiBytes+1), "bad utf8": "\xff", "control": "a\x01",
	} {
		_, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, emoji)
		if err == nil {
			t.Errorf("%s: accepted %q", name, emoji)
			continue
		}
		wantField(t, err, "emoji")
	}
	if _, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, strings.Repeat("x", MaxEmojiBytes)); err != nil {
		t.Fatalf("64 bytes: %v", err)
	}

	// Outsiders cannot react to a public channel's message without joining.
	_, err = e.svc.AddReaction(e.ctx, outsider.Actor, msg.ID, "👍")
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.AddReaction(e.ctx, alice.Actor, "missing", "👍")
	wantErr(t, err, ErrNotFound)

	// Deleted messages take no reactions.
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, msg.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "😀")
	wantErr(t, err, ErrConflict)
}

func TestAtMostFiftyDistinctReactions(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)
	msg := e.post(alice, ch.ID, "popular")
	for i := 0; i < MaxReactionsPerMessage; i++ {
		if _, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, fmt.Sprintf("e%d", i)); err != nil {
			t.Fatalf("reaction %d: %v", i, err)
		}
	}
	before := e.head()
	_, err := e.svc.AddReaction(e.ctx, alice.Actor, msg.ID, "one-too-many")
	wantField(t, err, "emoji")
	_, err = e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "another")
	wantField(t, err, "emoji")
	// Joining an existing emoji is not a new distinct one.
	if _, err := e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "e0"); err != nil {
		t.Fatalf("existing emoji at the limit: %v", err)
	}
	wantTypes(t, e.events(before), "reaction.added")
	// Removing the last use frees a slot.
	if _, err := e.svc.RemoveReaction(e.ctx, alice.Actor, msg.ID, "e1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AddReaction(e.ctx, bob.Actor, msg.ID, "another"); err != nil {
		t.Fatalf("after freeing a slot: %v", err)
	}
}

func TestPins(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	outsider := e.human("outsider", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPrivate, bob)
	other := e.channel(alice, "other", protocol.ChannelPublic)
	m1 := e.post(alice, ch.ID, "one")
	m2 := e.post(alice, ch.ID, "two")
	foreign := e.post(alice, other.ID, "elsewhere")

	// Any member may pin, not just the author or owner.
	before := e.head()
	pin, err := e.svc.PinMessage(e.ctx, bob.Actor, ch.ID, m1.ID)
	if err != nil || pin.Message.ID != m1.ID || pin.PinnedBy == nil || *pin.PinnedBy != bob.ID {
		t.Fatalf("PinMessage: %+v, %v", pin, err)
	}
	evs := e.events(before)
	wantTypes(t, evs, "pin.added")
	if evs[0].Channel != ch.ID {
		t.Fatalf("event channel = %q", evs[0].Channel)
	}
	before = e.head()
	if _, err := e.svc.PinMessage(e.ctx, alice.Actor, ch.ID, m1.ID); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))
	if _, err := e.svc.PinMessage(e.ctx, alice.Actor, ch.ID, m2.ID); err != nil {
		t.Fatal(err)
	}

	pins, err := e.svc.ListPins(e.ctx, bob.Actor, ch.ID)
	if err != nil || len(pins) != 2 || pins[0].Message.ID != m2.ID || pins[1].Message.ID != m1.ID {
		t.Fatalf("ListPins = %+v, %v", pins, err)
	}

	_, err = e.svc.PinMessage(e.ctx, bob.Actor, ch.ID, foreign.ID)
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.PinMessage(e.ctx, outsider.Actor, ch.ID, m1.ID)
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.ListPins(e.ctx, outsider.Actor, ch.ID)
	wantErr(t, err, ErrNotFound)

	before = e.head()
	if err := e.svc.UnpinMessage(e.ctx, bob.Actor, ch.ID, m1.ID); err != nil {
		t.Fatalf("UnpinMessage: %v", err)
	}
	wantTypes(t, e.events(before), "pin.removed")
	before = e.head()
	if err := e.svc.UnpinMessage(e.ctx, bob.Actor, ch.ID, m1.ID); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))

	// A deleted message cannot be pinned.
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, m1.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.PinMessage(e.ctx, bob.Actor, ch.ID, m1.ID)
	wantErr(t, err, ErrConflict)
}
