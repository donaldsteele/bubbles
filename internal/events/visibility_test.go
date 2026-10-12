package events

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// fakeMembership: channel id -> kind and members.
type fakeMembership map[string]struct {
	kind    string
	members []string
}

func (f fakeMembership) ChannelKind(id string) string { return f[id].kind }

func (f fakeMembership) IsMember(id, user string) bool {
	for _, m := range f[id].members {
		if m == user {
			return true
		}
	}
	return false
}

func (f fakeMembership) SharesChannel(a, b string) bool {
	for id := range f {
		if f.IsMember(id, a) && f.IsMember(id, b) {
			return true
		}
	}
	return false
}

func TestVisible(t *testing.T) {
	m := fakeMembership{
		"pub":  {protocol.ChannelPublic, []string{"alice", "gus"}},
		"priv": {protocol.ChannelPrivate, []string{"alice"}},
		"dm":   {protocol.ChannelDM, []string{"alice", "carol"}},
		"gdm":  {protocol.ChannelGroupDM, []string{"alice", "carol", "gus"}},
	}
	alice := Viewer{UserID: "alice", Role: protocol.RoleMember}
	bob := Viewer{UserID: "bob", Role: protocol.RoleAdmin}
	gus := Viewer{UserID: "gus", Role: protocol.RoleGuest}
	carol := Viewer{UserID: "carol", Role: protocol.RoleMember}
	bot := Viewer{UserID: "bot", Role: protocol.RoleMember, Kind: protocol.KindApp, ChannelIDs: []string{"priv"}}
	m["priv"] = struct {
		kind    string
		members []string
	}{protocol.ChannelPrivate, []string{"alice", "bot"}}

	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	tests := []struct {
		name string
		v    Viewer
		e    Event
		want bool
	}{
		{"member sees private message", alice, Event{Type: TypeMessageCreated, ChannelID: "priv"}, true},
		{"admin non-member never sees private message", bob, Event{Type: TypeMessageCreated, ChannelID: "priv"}, false},
		{"non-member never sees DM", bob, Event{Type: TypeMessageCreated, ChannelID: "dm"}, false},
		{"guest outside DM never sees it", gus, Event{Type: TypeReactionAdded, ChannelID: "dm"}, false},
		{"DM member sees it", carol, Event{Type: TypePinAdded, ChannelID: "dm"}, true},
		{"non-member does not get public messages", bob, Event{Type: TypeMessageCreated, ChannelID: "pub"}, false},
		{"public lifecycle to non-guest non-member", bob, Event{Type: TypeChannelCreated, ChannelID: "pub"}, true},
		{"public unarchive to non-guest", bob, Event{Type: TypeChannelUnarchived, ChannelID: "pub"}, true},
		{"public lifecycle to guest member", gus, Event{Type: TypeChannelUpdated, ChannelID: "pub"}, true},
		{"private lifecycle hidden from non-member", bob, Event{Type: TypeChannelArchived, ChannelID: "priv"}, false},
		{"DM created hidden from non-member", bob, Event{Type: TypeChannelCreated, ChannelID: "dm"}, false},
		{"group DM created to member guest", gus, Event{Type: TypeChannelCreated, ChannelID: "gdm"}, true},
		{"public lifecycle hidden from non-member guest", Viewer{UserID: "g2", Role: protocol.RoleGuest}, Event{Type: TypeChannelCreated, ChannelID: "pub"}, false},
		{"member_left reaches departing user", carol, Event{Type: TypeChannelMemberLeft, ChannelID: "priv", Payload: raw(`{"user_id":"carol"}`)}, true},
		{"member_left hidden from others", bob, Event{Type: TypeChannelMemberLeft, ChannelID: "priv", Payload: raw(`{"user_id":"carol"}`)}, false},
		{"member_joined to members", alice, Event{Type: TypeChannelMemberJoined, ChannelID: "dm"}, true},
		{"read.updated to its user", alice, Event{Type: TypeReadUpdated, UserID: "alice"}, true},
		{"read.updated hidden from others", bob, Event{Type: TypeReadUpdated, UserID: "alice"}, false},
		{"membership.updated to its user", bob, Event{Type: TypeMembershipUpdated, UserID: "bob"}, true},
		{"session.revoked hidden from others", alice, Event{Type: TypeSessionRevoked, UserID: "bob"}, false},
		{"ephemeral.message to its user", carol, Event{Type: TypeEphemeralMessage, UserID: "carol"}, true},
		{"command.invoked to owning app", bot, Event{Type: TypeCommandInvoked, UserID: "bot"}, true},
		{"command.invoked hidden from humans", alice, Event{Type: TypeCommandInvoked, UserID: "bot"}, false},
		{"user-scoped without user is hidden", alice, Event{Type: TypeReadUpdated}, false},
		{"settings.updated to everyone", gus, Event{Type: TypeSettingsUpdated}, true},
		{"user.updated to non-guest", bob, Event{Type: TypeUserUpdated, UserID: "carol"}, true},
		{"user.updated to self guest", gus, Event{Type: TypeUserUpdated, UserID: "gus"}, true},
		{"user.updated to guest sharing a channel", gus, Event{Type: TypeUserUpdated, Payload: raw(`{"id":"alice"}`)}, true},
		{"user.updated hidden from guest sharing nothing", gus, Event{Type: TypeUserUpdated, UserID: "bob"}, false},
		{"presence to guest sharing a channel", gus, Event{Type: TypePresenceChanged, UserID: "carol"}, true},
		{"presence hidden from guest sharing nothing", gus, Event{Type: TypePresenceChanged, UserID: "bob"}, false},
		{"typing to other member", gus, Event{Type: TypeTyping, ChannelID: "pub", UserID: "alice"}, true},
		{"typing not echoed to typist", alice, Event{Type: TypeTyping, ChannelID: "pub", UserID: "alice"}, false},
		{"typing hidden from non-member", bob, Event{Type: TypeTyping, ChannelID: "pub", UserID: "alice"}, false},
		{"alert.updated to members", alice, Event{Type: TypeAlertUpdated, ChannelID: "priv"}, true},
		{"app allowlist admits", bot, Event{Type: TypeMessageCreated, ChannelID: "priv"}, true},
		{"app allowlist excludes", bot, Event{Type: TypeChannelCreated, ChannelID: "pub"}, false},
		{"unknown type hidden", alice, Event{Type: "nope", ChannelID: "priv"}, false},
		{"channel event without channel hidden", alice, Event{Type: TypeMessageCreated}, false},
		{"anonymous viewer sees nothing", Viewer{}, Event{Type: TypeSettingsUpdated}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Visible(tt.v, tt.e, m); got != tt.want {
				t.Fatalf("Visible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMembershipCacheLoadsAndInvalidates(t *testing.T) {
	w := newWorld(t)
	w.user(t, "alice", protocol.RoleMember)
	w.user(t, "gus", protocol.RoleGuest)
	w.channel(t, "pub", protocol.ChannelPublic, "alice")
	c := NewMembershipCache(context.Background(), w.db.Read(), nil)
	if c.ChannelKind("pub") != protocol.ChannelPublic || !c.IsMember("pub", "alice") || c.IsMember("pub", "gus") {
		t.Fatal("initial load wrong")
	}
	if c.ChannelKind("missing") != "" || c.IsMember("missing", "alice") {
		t.Fatal("missing channel must be unknown")
	}
	if c.SharesChannel("alice", "gus") {
		t.Fatal("alice and gus share nothing yet")
	}
	w.exec(t, `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES ('pub', 'gus', 0)`)
	if c.IsMember("pub", "gus") {
		t.Fatal("cache should still hold the old member list")
	}
	c.InvalidateChannel("pub")
	c.InvalidateUser("gus")
	if !c.IsMember("pub", "gus") || !c.SharesChannel("alice", "gus") {
		t.Fatal("invalidation did not reload")
	}
	c.InvalidateAll()
	if got := len(c.Members("pub")); got != 2 {
		t.Fatalf("members = %d, want 2", got)
	}
}
