package chat

import (
	"errors"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TestAlertAndSystemMessagesNeverMention: alert and system bodies come from
// apps and resolvers, so no mention row or flag is stored for them, on create
// or on a server-side body refresh.
func TestAlertAndSystemMessagesNeverMention(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleAdmin)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(owner, "ops", protocol.ChannelPublic, bob)

	noMentions := func(id string) {
		t.Helper()
		if n := e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, id); n != 0 {
			t.Errorf("message %s: %d mention rows, want 0", id, n)
		}
		if n := e.count(`SELECT COUNT(*) FROM messages WHERE id = ? AND (mention_channel = 1 OR mention_here = 1)`, id); n != 0 {
			t.Errorf("message %s: mention flags set", id)
		}
	}

	for _, kind := range []string{protocol.MessageAlert, protocol.MessageSystem} {
		var msg protocol.Message
		err := e.db.Do(e.ctx, func(tx *store.Tx) error {
			var err error
			msg, err = e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: ch.ID, AuthorID: owner.ID, Kind: kind, Body: "@bob @channel @here"})
			return err
		})
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		if msg.Mentions.Channel || msg.Mentions.Here || len(msg.Mentions.UserIDs) != 0 {
			t.Errorf("%s message mentions = %+v, want none", kind, msg.Mentions)
		}
		noMentions(msg.ID)

		err = e.db.Do(e.ctx, func(tx *store.Tx) error {
			return e.svc.SetBodyTx(tx, msg.ID, "@bob @channel again")
		})
		if err != nil {
			t.Fatalf("SetBodyTx %s: %v", kind, err)
		}
		noMentions(msg.ID)
	}

	// A text message still mentions, so the rule is about kind, not the body.
	if text := e.post(owner, ch.ID, "hi @bob"); len(text.Mentions.UserIDs) != 1 {
		t.Errorf("text message mentions = %+v, want bob", text.Mentions)
	}
}

// TestAppMessageBodiesNeedMessagesRead: every read that returns message bodies
// needs messages:read for an app. channels:read shows the channel only (403 on
// a visible channel); an app outside the channel gets 404.
func TestAppMessageBodiesNeedMessagesRead(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleAdmin)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)
	outsider := e.addUser("outsider", protocol.RoleMember, protocol.KindApp)
	ch := e.channel(owner, "talk", protocol.ChannelPrivate, bot)
	root := e.post(owner, ch.ID, "secret body")
	reply := e.reply(owner, ch.ID, root.ID, "secret reply")
	if _, err := e.svc.PinMessage(e.ctx, owner.Actor, ch.ID, root.ID); err != nil {
		t.Fatalf("pin: %v", err)
	}

	reads := func(a Actor) map[string]error {
		_, lm := e.svc.ListMessages(e.ctx, a, ch.ID, PageOptions{})
		_, lt := e.svc.ListThread(e.ctx, a, reply.ID, PageOptions{})
		_, gm := e.svc.GetMessage(e.ctx, a, root.ID)
		_, lp := e.svc.ListPins(e.ctx, a, ch.ID)
		_, pin := e.svc.PinMessage(e.ctx, a, ch.ID, root.ID)
		return map[string]error{"ListMessages": lm, "ListThread": lt, "GetMessage": gm, "ListPins": lp, "PinMessage": pin}
	}

	channelsOnly := bot.Actor
	channelsOnly.Scopes = []string{ScopeChannelsRead}
	for name, err := range reads(channelsOnly) {
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("channels:read %s: err = %v, want forbidden", name, err)
		}
	}
	if _, err := e.svc.GetChannel(e.ctx, channelsOnly, ch.ID); err != nil {
		t.Errorf("channels:read GetChannel: %v", err)
	}

	writeOnly := bot.Actor
	writeOnly.Scopes = []string{ScopeMessagesWrite}
	if _, err := e.svc.PinMessage(e.ctx, writeOnly, ch.ID, root.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("messages:write-only pin: err = %v, want forbidden", err)
	}

	reader := bot.Actor
	reader.Scopes = []string{ScopeMessagesRead}
	if _, err := e.svc.ListMessages(e.ctx, reader, ch.ID, PageOptions{}); err != nil {
		t.Errorf("messages:read ListMessages: %v", err)
	}
	if _, err := e.svc.ListThread(e.ctx, reader, reply.ID, PageOptions{}); err != nil {
		t.Errorf("messages:read ListThread: %v", err)
	}
	if got, err := e.svc.GetMessage(e.ctx, reader, reply.ID); err != nil || got.Body != "secret reply" {
		t.Errorf("messages:read GetMessage = %q, %v", got.Body, err)
	}
	if pins, err := e.svc.ListPins(e.ctx, reader, ch.ID); err != nil || len(pins) != 1 {
		t.Errorf("messages:read ListPins = %d, %v", len(pins), err)
	}

	outsider.Actor.Scopes = []string{ScopeMessagesRead}
	for name, err := range reads(outsider.Actor) {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("outside the channel %s: err = %v, want not found", name, err)
		}
	}
}
