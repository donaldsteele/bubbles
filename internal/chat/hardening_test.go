package chat

import (
	"errors"
	"slices"
	"testing"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// wantNonceError fails unless err is a validation error on the nonce field.
func wantNonceError(t *testing.T, err error) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Fields["nonce"] == "" {
		t.Fatalf("err = %v, want a validation error on nonce", err)
	}
}

// sameIDs fails unless got and want hold the same ids, in any order.
func sameIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// bootstrapIDs lists the ids a bootstrap snapshot carries in each collection.
func bootstrapIDs(b protocol.Bootstrap) (channels, memberships, unread, users []string) {
	for _, c := range b.Channels {
		channels = append(channels, c.ID)
	}
	for _, m := range b.Memberships {
		memberships = append(memberships, m.ChannelID)
	}
	for _, u := range b.Unread {
		unread = append(unread, u.ChannelID)
	}
	for _, u := range b.Users {
		users = append(users, u.ID)
	}
	return channels, memberships, unread, users
}

func TestNonceCannotReachAnotherChannel(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	vault := e.channel(alice, "vault", protocol.ChannelPublic)
	lobby := e.channel(alice, "lobby", protocol.ChannelPublic)
	first, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: vault.ID, Body: "vault-body", Nonce: "n-x"})
	if err != nil || !created {
		t.Fatalf("first: %v created=%v", err, created)
	}

	// The same channel still replays the original message.
	again, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: vault.ID, Body: "retry", Nonce: "n-x"})
	if err != nil || created || again.ID != first.ID || again.Body != "vault-body" {
		t.Fatalf("same-channel replay: %+v created=%v err=%v", again, created, err)
	}

	// Another channel gets a validation error and none of the stored message.
	msg, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: lobby.ID, Body: "probe", Nonce: "n-x"})
	wantNonceError(t, err)
	if created || msg.ID != "" || msg.Body != "" {
		t.Fatalf("cross-channel replay returned %+v created=%v", msg, created)
	}
	var trusted protocol.Message
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		var err error
		trusted, err = e.svc.CreateMessageTx(tx, CreateMessageParams{ChannelID: lobby.ID, AuthorID: alice.ID, Body: "probe", Nonce: "n-x"})
		return err
	})
	wantNonceError(t, err)
	if trusted.ID != "" || trusted.Body != "" {
		t.Fatalf("trusted cross-channel replay returned %+v", trusted)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE channel_id = ?`, lobby.ID); n != 0 {
		t.Fatalf("%d messages in lobby, want 0", n)
	}
}

func TestNonceReplayAfterRemoveMemberIsNotFound(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	vault := e.channel(alice, "vault", protocol.ChannelPrivate, bob)
	p := CreateMessageParams{ChannelID: vault.ID, Body: "vault-body", Nonce: "n-v"}
	if _, created, err := e.svc.CreateMessage(e.ctx, bob.Actor, p); err != nil || !created {
		t.Fatalf("post: %v created=%v", err, created)
	}
	if err := e.svc.RemoveMember(e.ctx, alice.Actor, vault.ID, bob.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	msg, created, err := e.svc.CreateMessage(e.ctx, bob.Actor, p)
	if !errors.Is(err, ErrNotFound) || created || msg.ID != "" || msg.Body != "" {
		t.Fatalf("replay after removal: %+v created=%v err=%v", msg, created, err)
	}
}

func TestNonceReplayMustUseSameThread(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic)
	root := e.post(alice, ch.ID, "root")
	reply, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "reply", ThreadRootID: root.ID, Nonce: "n-t"})
	if err != nil || !created {
		t.Fatalf("reply: %v created=%v", err, created)
	}
	// Addressing the same thread through the reply itself resolves to the same root.
	again, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "retry", ThreadRootID: reply.ID, Nonce: "n-t"})
	if err != nil || created || again.ID != reply.ID {
		t.Fatalf("same-thread replay: %+v created=%v err=%v", again, created, err)
	}
	// The same nonce at the top level is a different post and is refused.
	msg, created, err := e.svc.CreateMessage(e.ctx, alice.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "top", Nonce: "n-t"})
	wantNonceError(t, err)
	if created || msg.ID != "" {
		t.Fatalf("other-thread replay returned %+v", msg)
	}
}

func TestBootstrapAppAllowlist(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleAdmin)
	peer := e.human("peer", protocol.RoleMember)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)
	allowed := e.channel(owner, "allowed", protocol.ChannelPrivate, bot)
	other := e.channel(owner, "other", protocol.ChannelPrivate, bot, peer)
	e.post(owner, allowed.ID, "one")
	e.post(owner, other.ID, "two")

	scoped := bot.Actor
	scoped.ChannelIDs = []string{allowed.ID}
	scoped.Scopes = []string{ScopeMessagesRead}
	b, err := e.svc.Bootstrap(e.ctx, scoped)
	if err != nil {
		t.Fatalf("Bootstrap(allowlist): %v", err)
	}
	ch, mb, un, us := bootstrapIDs(b)
	sameIDs(t, "allowlisted channels", ch, []string{allowed.ID})
	sameIDs(t, "allowlisted memberships", mb, []string{allowed.ID})
	sameIDs(t, "allowlisted unread", un, []string{allowed.ID})
	sameIDs(t, "allowlisted users", us, []string{bot.ID, owner.ID})

	unrestricted := bot.Actor
	unrestricted.Scopes = []string{ScopeMessagesRead}
	b, err = e.svc.Bootstrap(e.ctx, unrestricted)
	if err != nil {
		t.Fatalf("Bootstrap(no allowlist): %v", err)
	}
	ch, mb, un, us = bootstrapIDs(b)
	sameIDs(t, "channels", ch, []string{allowed.ID, other.ID})
	sameIDs(t, "memberships", mb, []string{allowed.ID, other.ID})
	sameIDs(t, "unread", un, []string{allowed.ID, other.ID})
	sameIDs(t, "users", us, []string{bot.ID, owner.ID, peer.ID})
}

func TestAddAppToChannelNeedsAdmin(t *testing.T) {
	e := newEnv(t)
	admin := e.human("admin", protocol.RoleAdmin)
	owner := e.human("owner", protocol.RoleMember)
	peer := e.human("peer", protocol.RoleMember)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)
	ch := e.channel(owner, "talk", protocol.ChannelPublic)

	if _, err := e.svc.AddMember(e.ctx, owner.Actor, ch.ID, bot.ID, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner adding an app: err = %v, want forbidden", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, ch.ID, bot.ID); n != 0 {
		t.Fatalf("%d memberships for the app after a refused add, want 0", n)
	}
	if _, err := e.svc.AddMember(e.ctx, owner.Actor, ch.ID, peer.ID, ""); err != nil {
		t.Fatalf("owner adding a human: %v", err)
	}
	if _, err := e.svc.AddMember(e.ctx, admin.Actor, ch.ID, bot.ID, ""); err != nil {
		t.Fatalf("admin adding an app: %v", err)
	}
	if _, err := e.svc.AddMember(e.ctx, admin.Actor, ch.ID, bot.ID, protocol.MemberOwner); !errors.Is(err, ErrValidation) {
		t.Fatalf("app as owner: err = %v, want validation", err)
	}
}

func TestOnUserCreatedJoinsOnlyHumans(t *testing.T) {
	cases := []struct {
		name, role, kind string
		joins            bool
	}{
		{"human member", protocol.RoleMember, protocol.KindHuman, true},
		{"human admin", protocol.RoleAdmin, protocol.KindHuman, true},
		{"human guest", protocol.RoleGuest, protocol.KindHuman, false},
		{"app member", protocol.RoleMember, protocol.KindApp, false},
		{"app admin", protocol.RoleAdmin, protocol.KindApp, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			owner := e.human("owner", protocol.RoleAdmin)
			lobby := e.channel(owner, "lobby", protocol.ChannelPublic)
			if _, err := e.svc.SetDefault(e.ctx, owner.Actor, lobby.ID, true); err != nil {
				t.Fatalf("SetDefault: %v", err)
			}
			u := e.addUser("newbie", tc.role, tc.kind)
			if err := e.db.Do(e.ctx, func(tx *store.Tx) error { return e.svc.OnUserCreatedTx(tx.Context(), tx, u.ID) }); err != nil {
				t.Fatalf("OnUserCreatedTx: %v", err)
			}
			want := 0
			if tc.joins {
				want = 1
			}
			if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, lobby.ID, u.ID); n != want {
				t.Fatalf("%d memberships, want %d", n, want)
			}
		})
	}
}

func TestDeleteScrubsStoredEvents(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	ch := e.channel(alice, "talk", protocol.ChannelPublic, bob)
	const secret = "zebra-secret-body"
	m, _, err := e.svc.CreateMessage(e.ctx, bob.Actor, CreateMessageParams{ChannelID: ch.ID, Body: secret, Nonce: "n-s"})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := e.svc.EditMessage(e.ctx, bob.Actor, m.ID, secret+" v2"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, err := e.svc.AddReaction(e.ctx, alice.Actor, m.ID, "👍"); err != nil {
		t.Fatalf("react: %v", err)
	}
	if _, err := e.svc.PinMessage(e.ctx, alice.Actor, ch.ID, m.ID); err != nil {
		t.Fatalf("pin: %v", err)
	}
	const leaked = `SELECT COUNT(*) FROM events WHERE payload LIKE ?`
	if n := e.count(leaked, "%"+secret+"%"); n == 0 {
		t.Fatal("the body never reached the event log")
	}

	if err := e.svc.DeleteMessage(e.ctx, bob.Actor, m.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n := e.count(leaked, "%"+secret+"%"); n != 0 {
		t.Fatalf("%d stored events still carry the body after delete", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type IN ('message.created', 'message.updated')
		AND json_extract(payload, '$.id') = ? AND json_extract(payload, '$.deleted') = 1 AND json_extract(payload, '$.body') = ''`, m.ID); n != 2 {
		t.Fatalf("%d tombstoned message events, want 2 (created, edited)", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type IN ('reaction.added', 'pin.added')
		AND json_extract(payload, '$.message_id') = ? AND json_extract(payload, '$.deleted') = 1`, m.ID); n != 2 {
		t.Fatalf("%d tombstoned reaction and pin events, want 2", n)
	}
}

func TestAppReadStateNeedsScopes(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleAdmin)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)
	ch := e.channel(owner, "talk", protocol.ChannelPublic, bot)
	m := e.post(owner, ch.ID, "hello")
	notify := MembershipUpdate{NotifyLevel: ptr(protocol.NotifyNone)}

	bare := bot.Actor
	bare.Scopes = nil
	if _, err := e.svc.ListChannels(e.ctx, bare, ScopeJoined); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListChannels without scopes: err = %v, want forbidden", err)
	}
	if _, err := e.svc.MarkRead(e.ctx, bare, ch.ID, m.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("MarkRead without scopes: err = %v, want forbidden", err)
	}
	if _, err := e.svc.UpdateMembership(e.ctx, bare, ch.ID, notify); !errors.Is(err, ErrForbidden) {
		t.Fatalf("UpdateMembership without scopes: err = %v, want forbidden", err)
	}

	reader := bot.Actor
	reader.Scopes = []string{ScopeChannelsRead}
	if got, err := e.svc.ListChannels(e.ctx, reader, ScopeJoined); err != nil || len(got) != 1 {
		t.Fatalf("ListChannels with channels:read: %d channels, err = %v", len(got), err)
	}
	if _, err := e.svc.UpdateMembership(e.ctx, reader, ch.ID, notify); err != nil {
		t.Fatalf("UpdateMembership with channels:read: %v", err)
	}
	if _, err := e.svc.MarkRead(e.ctx, reader, ch.ID, m.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("MarkRead with channels:read only: err = %v, want forbidden", err)
	}

	history := bot.Actor
	history.Scopes = []string{ScopeMessagesRead}
	if _, err := e.svc.MarkRead(e.ctx, history, ch.ID, m.ID); err != nil {
		t.Fatalf("MarkRead with messages:read: %v", err)
	}
}
