package chat

import (
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func unreadOf(t *testing.T, e *env, u user, channelID string) protocol.Unread {
	t.Helper()
	all, err := e.svc.UnreadCounts(e.ctx, u.ID)
	if err != nil {
		t.Fatalf("UnreadCounts: %v", err)
	}
	for _, c := range all {
		if c.ChannelID == channelID {
			return c
		}
	}
	t.Fatalf("no unread entry for channel %s", channelID)
	return protocol.Unread{}
}

func TestUnreadExcludesOwnAndSystemMessages(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)

	e.post(alice, ch.ID, "from alice 1")
	e.post(alice, ch.ID, "from alice 2")
	own := e.post(bob, ch.ID, "from bob")
	if got := unreadOf(t, e, bob, ch.ID); got.Unread != 2 || got.Mentions != 0 {
		t.Fatalf("bob: %+v, want 2 unread (his own message does not count)", got)
	}
	if got := unreadOf(t, e, alice, ch.ID); got.Unread != 1 {
		t.Fatalf("alice: %+v, want 1 unread", got)
	}

	// System messages never count.
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: ch.ID, AuthorID: alice.ID, Kind: protocol.MessageSystem, Body: "alice did something"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, e, bob, ch.ID); got.Unread != 2 {
		t.Fatalf("bob after a system message: %+v, want 2", got)
	}

	// Thread replies count; replies from himself do not; deleted messages do not.
	root := e.post(alice, ch.ID, "root")
	e.reply(alice, ch.ID, root.ID, "reply 1")
	e.reply(bob, ch.ID, root.ID, "my reply")
	doomed := e.post(alice, ch.ID, "oops")
	if got := unreadOf(t, e, bob, ch.ID); got.Unread != 5 {
		t.Fatalf("bob with threads: %+v, want 5", got)
	}
	if err := e.svc.DeleteMessage(e.ctx, alice.Actor, doomed.ID); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, e, bob, ch.ID); got.Unread != 4 {
		t.Fatalf("bob after a delete: %+v, want 4", got)
	}

	// Alerts from apps count, mentions are counted separately.
	e.post(alice, ch.ID, "hey @bob")
	e.post(alice, ch.ID, "@channel standup")
	e.post(alice, ch.ID, "@here lunch")
	e.post(alice, ch.ID, "`@bob` in code is not a mention")
	got := unreadOf(t, e, bob, ch.ID)
	if got.Unread != 8 || got.Mentions != 3 {
		t.Fatalf("bob: %+v, want 8 unread and 3 mentions", got)
	}
	// Alice's own @mention of herself would not count either.
	e.post(bob, ch.ID, "@alice ping")
	if got := unreadOf(t, e, alice, ch.ID); got.Mentions != 1 {
		t.Fatalf("alice mentions = %d, want 1", got.Mentions)
	}

	// Reading everything clears it.
	last := e.post(alice, ch.ID, "last")
	if _, err := e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, last.ID); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, e, bob, ch.ID); got.Unread != 0 || got.Mentions != 0 {
		t.Fatalf("after reading: %+v", got)
	}
	_ = own
}

func TestJoiningDoesNotMakeHistoryUnread(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	late := e.human("late", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	for i := 0; i < 3; i++ {
		e.post(alice, ch.ID, "old")
	}
	if _, err := e.svc.JoinChannel(e.ctx, late.Actor, ch.ID); err != nil {
		t.Fatal(err)
	}
	if got := unreadOf(t, e, late, ch.ID); got.Unread != 0 {
		t.Fatalf("history before joining is unread: %+v", got)
	}
	e.post(alice, ch.ID, "new")
	if got := unreadOf(t, e, late, ch.ID); got.Unread != 1 {
		t.Fatalf("after one new message: %+v", got)
	}
}

func TestMarkReadOnlyMovesForward(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	outsider := e.human("outsider", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPrivate, bob)
	other := e.channel(alice, "other", protocol.ChannelPrivate, bob)
	var ids []string
	for i := 0; i < 4; i++ {
		ids = append(ids, e.post(alice, ch.ID, "m").ID)
	}
	foreign := e.post(alice, other.ID, "elsewhere")

	before := e.head()
	ev, err := e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, ids[1])
	if err != nil || ev.LastReadID != ids[1] || ev.Unread != 2 || ev.ChannelID != ch.ID {
		t.Fatalf("MarkRead: %+v, %v", ev, err)
	}
	evs := e.events(before)
	wantTypes(t, evs, "read.updated")
	if evs[0].User != bob.ID || evs[0].Channel != "" || !strings.Contains(evs[0].Payload, ids[1]) {
		t.Fatalf("read.updated = %+v", evs[0])
	}

	// Backwards and sideways moves change nothing.
	for _, id := range []string{ids[0], ids[1]} {
		before = e.head()
		ev, err = e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, id)
		if err != nil || ev.LastReadID != ids[1] || ev.Unread != 2 {
			t.Fatalf("MarkRead(%s): %+v, %v", id, ev, err)
		}
		wantTypes(t, e.events(before))
	}
	ev, err = e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, ids[3])
	if err != nil || ev.LastReadID != ids[3] || ev.Unread != 0 {
		t.Fatalf("forward: %+v, %v", ev, err)
	}

	_, err = e.svc.MarkRead(e.ctx, bob.Actor, ch.ID, foreign.ID)
	wantField(t, err, "message_id")
	_, err = e.svc.MarkRead(e.ctx, outsider.Actor, ch.ID, ids[0])
	wantErr(t, err, ErrNotFound)
	pub := e.channel(alice, "pub", protocol.ChannelPublic)
	pm := e.post(alice, pub.ID, "x")
	_, err = e.svc.MarkRead(e.ctx, outsider.Actor, pub.ID, pm.ID)
	wantErr(t, err, ErrForbidden)
}
