package chat

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func wantErr(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func wantField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want a ValidationError", err)
	}
	if _, ok := ve.Fields[field]; !ok {
		t.Fatalf("validation fields = %v, want %q", ve.Fields, field)
	}
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("ValidationError does not match ErrValidation")
	}
}

func TestCreateChannel(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	gone := e.human("gone", protocol.RoleMember)
	e.deactivate(gone)

	before := e.head()
	ch, err := e.svc.CreateChannel(e.ctx, alice.Actor, CreateChannelParams{
		Name: " #Dev-Team ", Kind: protocol.ChannelPrivate, Topic: "builds", MemberIDs: []string{bob.ID, bob.ID, alice.ID},
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if ch.Name == nil || *ch.Name != "dev-team" || ch.Kind != protocol.ChannelPrivate || ch.Topic != "builds" || ch.MemberCount != 2 {
		t.Fatalf("channel = %+v", ch)
	}
	evs := e.events(before)
	wantTypes(t, evs, "channel.created")
	if evs[0].Channel != ch.ID || evs[0].User != "" {
		t.Fatalf("event scope = channel %q user %q", evs[0].Channel, evs[0].User)
	}
	roles := map[string]string{}
	for _, m := range mustMembers(t, e, alice, ch.ID) {
		roles[m.UserID] = m.Role
	}
	if roles[alice.ID] != protocol.MemberOwner || roles[bob.ID] != protocol.MemberMember || len(roles) != 2 {
		t.Fatalf("roles = %v", roles)
	}

	bad := []struct {
		name   string
		p      CreateChannelParams
		field  string
		target error
	}{
		{"empty name", CreateChannelParams{Name: " ", Kind: protocol.ChannelPublic}, "name", nil},
		{"uppercase symbols", CreateChannelParams{Name: "a b", Kind: protocol.ChannelPublic}, "name", nil},
		{"underscore", CreateChannelParams{Name: "a_b", Kind: protocol.ChannelPublic}, "name", nil},
		{"too long", CreateChannelParams{Name: strings.Repeat("a", 65), Kind: protocol.ChannelPublic}, "name", nil},
		{"kind dm", CreateChannelParams{Name: "x", Kind: protocol.ChannelDM}, "kind", nil},
		{"kind empty", CreateChannelParams{Name: "x"}, "kind", nil},
		{"topic", CreateChannelParams{Name: "x", Kind: protocol.ChannelPublic, Topic: strings.Repeat("t", 251)}, "topic", nil},
		{"description", CreateChannelParams{Name: "x", Kind: protocol.ChannelPublic, Description: strings.Repeat("d", 1001)}, "description", nil},
		{"unknown member", CreateChannelParams{Name: "x", Kind: protocol.ChannelPublic, MemberIDs: []string{"nope"}}, "member_ids", nil},
		{"deactivated member", CreateChannelParams{Name: "x", Kind: protocol.ChannelPublic, MemberIDs: []string{gone.ID}}, "member_ids", nil},
		{"taken (private names count)", CreateChannelParams{Name: "DEV-team", Kind: protocol.ChannelPublic}, "", ErrConflict},
	}
	for _, c := range bad {
		head := e.head()
		_, err := e.svc.CreateChannel(e.ctx, alice.Actor, c.p)
		if c.target != nil {
			wantErr(t, err, c.target)
		} else {
			wantField(t, err, c.field)
		}
		if e.head() != head {
			t.Errorf("%s: a failed create emitted events", c.name)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM channels`); n != 1 {
		t.Fatalf("%d channels, want 1", n)
	}

	guest := e.human("guest", protocol.RoleGuest)
	_, err = e.svc.CreateChannel(e.ctx, guest.Actor, CreateChannelParams{Name: "g", Kind: protocol.ChannelPublic})
	wantErr(t, err, ErrForbidden)
}

func mustMembers(t *testing.T, e *env, a user, channelID string) []protocol.Member {
	t.Helper()
	ms, err := e.svc.ListMembers(e.ctx, a.Actor, channelID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	return ms
}

func TestUpdateChannel(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	other := e.human("other", protocol.RoleMember)
	admin := e.human("root", protocol.RoleAdmin)
	ch := e.channel(owner, "ops", protocol.ChannelPublic, other)
	e.channel(owner, "taken", protocol.ChannelPublic)

	before := e.head()
	got, err := e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{
		Name: ptr("Ops-2"), Topic: ptr("new topic"), Description: ptr("about"), RetentionDays: Some(30),
	})
	if err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if *got.Name != "ops-2" || got.Topic != "new topic" || got.Description != "about" || got.RetentionDays == nil || *got.RetentionDays != 30 {
		t.Fatalf("channel = %+v", got)
	}
	wantTypes(t, e.events(before), "channel.updated")

	// Nothing changed: no event.
	before = e.head()
	if _, err := e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{Topic: ptr("new topic")}); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))

	// Retention can be cleared; admins manage public channels they are not in.
	got, err = e.svc.UpdateChannel(e.ctx, admin.Actor, ch.ID, UpdateChannelParams{RetentionDays: Clear[int]()})
	if err != nil || got.RetentionDays != nil {
		t.Fatalf("clear retention: %+v, %v", got, err)
	}

	_, err = e.svc.UpdateChannel(e.ctx, other.Actor, ch.ID, UpdateChannelParams{Topic: ptr("hijack")})
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{Name: ptr("taken")})
	wantErr(t, err, ErrConflict)
	_, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{Name: ptr("bad name")})
	wantField(t, err, "name")
	_, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{RetentionDays: Some(0)})
	wantField(t, err, "retention_days")

	// The retention ceiling is the server's default retention.
	if err := e.setRetentionCeiling(90); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{RetentionDays: Some(91)})
	wantField(t, err, "retention_days")
	if _, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{RetentionDays: Some(90)}); err != nil {
		t.Fatalf("retention at the ceiling: %v", err)
	}
}

func TestArchiveBlocksMessages(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	member := e.human("member", protocol.RoleMember)
	ch := e.channel(owner, "old", protocol.ChannelPublic, member)
	msg := e.post(member, ch.ID, "before")

	before := e.head()
	got, err := e.svc.ArchiveChannel(e.ctx, owner.Actor, ch.ID)
	if err != nil || got.ArchivedAt == nil {
		t.Fatalf("ArchiveChannel: %+v, %v", got, err)
	}
	wantTypes(t, e.events(before), "channel.archived")
	_, err = e.svc.ArchiveChannel(e.ctx, owner.Actor, ch.ID)
	wantErr(t, err, ErrConflict)

	_, _, err = e.svc.CreateMessage(e.ctx, member.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "after"})
	wantErr(t, err, ErrConflict)
	_, err = e.svc.EditMessage(e.ctx, member.Actor, msg.ID, "edited")
	wantErr(t, err, ErrConflict)
	_, err = e.svc.AddReaction(e.ctx, member.Actor, msg.ID, "👍")
	wantErr(t, err, ErrConflict)
	_, err = e.svc.PinMessage(e.ctx, member.Actor, ch.ID, msg.ID)
	wantErr(t, err, ErrConflict)
	wantErr(t, e.svc.DeleteMessage(e.ctx, member.Actor, msg.ID), ErrConflict)
	_, err = e.svc.UpdateChannel(e.ctx, owner.Actor, ch.ID, UpdateChannelParams{Topic: ptr("x")})
	wantErr(t, err, ErrConflict)
	if _, err := e.svc.GetMessage(e.ctx, member.Actor, msg.ID); err != nil {
		t.Fatalf("archived channels stay readable: %v", err)
	}
	// A new member cannot join an archived channel, but members can leave.
	late := e.human("late", protocol.RoleMember)
	_, err = e.svc.JoinChannel(e.ctx, late.Actor, ch.ID)
	wantErr(t, err, ErrConflict)
	if err := e.svc.LeaveChannel(e.ctx, member.Actor, ch.ID); err != nil {
		t.Fatalf("leave archived: %v", err)
	}

	before = e.head()
	got, err = e.svc.UnarchiveChannel(e.ctx, owner.Actor, ch.ID)
	if err != nil || got.ArchivedAt != nil {
		t.Fatalf("UnarchiveChannel: %+v, %v", got, err)
	}
	wantTypes(t, e.events(before), "channel.unarchived")
	_, err = e.svc.UnarchiveChannel(e.ctx, owner.Actor, ch.ID)
	wantErr(t, err, ErrConflict)
	if _, _, err := e.svc.CreateMessage(e.ctx, owner.Actor, CreateMessageParams{ChannelID: ch.ID, Body: "again"}); err != nil {
		t.Fatalf("post after unarchive: %v", err)
	}
}

func TestJoinLeaveAddRemove(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleMember)
	guest := e.human("guest", protocol.RoleGuest)
	gone := e.human("gone", protocol.RoleMember)
	e.deactivate(gone)
	pub := e.channel(owner, "pub", protocol.ChannelPublic)
	priv := e.channel(owner, "priv", protocol.ChannelPrivate)

	// Join: public yes, private is invisible, guests cannot see the public one.
	before := e.head()
	m, err := e.svc.JoinChannel(e.ctx, bob.Actor, pub.ID)
	if err != nil || m.Role != protocol.MemberMember || m.NotifyLevel != protocol.NotifyMentions {
		t.Fatalf("JoinChannel: %+v, %v", m, err)
	}
	wantTypes(t, e.events(before), "channel.member_joined")
	before = e.head()
	if _, err := e.svc.JoinChannel(e.ctx, bob.Actor, pub.ID); err != nil {
		t.Fatalf("second join: %v", err)
	}
	wantTypes(t, e.events(before))
	_, err = e.svc.JoinChannel(e.ctx, bob.Actor, priv.ID)
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.JoinChannel(e.ctx, guest.Actor, pub.ID)
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.GetChannel(e.ctx, bob.Actor, priv.ID)
	wantErr(t, err, ErrNotFound)

	// Leave.
	before = e.head()
	if err := e.svc.LeaveChannel(e.ctx, bob.Actor, pub.ID); err != nil {
		t.Fatalf("LeaveChannel: %v", err)
	}
	evs := e.events(before)
	wantTypes(t, evs, "channel.member_left")
	wantErr(t, e.svc.LeaveChannel(e.ctx, bob.Actor, pub.ID), ErrForbidden)

	// Add members: only owners; guests can be added; deactivated and unknown users cannot.
	_, err = e.svc.AddMember(e.ctx, bob.Actor, priv.ID, carol.ID, "")
	wantErr(t, err, ErrNotFound)
	if _, err := e.svc.JoinChannel(e.ctx, bob.Actor, pub.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.AddMember(e.ctx, bob.Actor, pub.ID, carol.ID, "")
	wantErr(t, err, ErrForbidden)
	before = e.head()
	if _, err := e.svc.AddMember(e.ctx, owner.Actor, priv.ID, carol.ID, ""); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if _, err := e.svc.AddMember(e.ctx, owner.Actor, priv.ID, guest.ID, ""); err != nil {
		t.Fatalf("AddMember guest: %v", err)
	}
	wantTypes(t, e.events(before), "channel.member_joined", "channel.member_joined")
	_, err = e.svc.AddMember(e.ctx, owner.Actor, priv.ID, gone.ID, "")
	wantField(t, err, "user_id")
	_, err = e.svc.AddMember(e.ctx, owner.Actor, priv.ID, "nope", "")
	wantField(t, err, "user_id")
	_, err = e.svc.AddMember(e.ctx, owner.Actor, priv.ID, carol.ID, "boss")
	wantField(t, err, "role")
	_, err = e.svc.AddMember(e.ctx, owner.Actor, priv.ID, guest.ID, protocol.MemberOwner)
	wantField(t, err, "role")

	// Adding again is a no-op; a different role promotes (user-scoped event) and the new
	// owner may manage the channel.
	before = e.head()
	if _, err := e.svc.AddMember(e.ctx, owner.Actor, priv.ID, carol.ID, protocol.MemberMember); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))
	m, err = e.svc.AddMember(e.ctx, owner.Actor, priv.ID, carol.ID, protocol.MemberOwner)
	if err != nil || m.Role != protocol.MemberOwner {
		t.Fatalf("promote: %+v, %v", m, err)
	}
	evs = e.events(before)
	wantTypes(t, evs, "membership.updated")
	if evs[0].User != carol.ID || evs[0].Channel != "" {
		t.Fatalf("membership.updated scope: user %q channel %q", evs[0].User, evs[0].Channel)
	}
	if _, err := e.svc.UpdateChannel(e.ctx, carol.Actor, priv.ID, UpdateChannelParams{Topic: ptr("by carol")}); err != nil {
		t.Fatalf("new owner manages: %v", err)
	}

	// Remove: owners remove others, members cannot, removing oneself is leaving.
	_, err = e.svc.ListMembers(e.ctx, bob.Actor, priv.ID)
	wantErr(t, err, ErrNotFound)
	wantErr(t, e.svc.RemoveMember(e.ctx, guest.Actor, priv.ID, carol.ID), ErrForbidden)
	before = e.head()
	if err := e.svc.RemoveMember(e.ctx, owner.Actor, priv.ID, guest.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	evs = e.events(before)
	wantTypes(t, evs, "channel.member_left")
	wantErr(t, e.svc.RemoveMember(e.ctx, owner.Actor, priv.ID, guest.ID), ErrNotFound)
	before = e.head()
	if err := e.svc.RemoveMember(e.ctx, carol.Actor, priv.ID, carol.ID); err != nil {
		t.Fatalf("remove self: %v", err)
	}
	wantTypes(t, e.events(before), "channel.member_left")
}

func TestAdminPowers(t *testing.T) {
	e := newEnv(t)
	admin := e.human("root", protocol.RoleAdmin)
	owner := e.human("owner", protocol.RoleMember)
	pub := e.channel(owner, "pub", protocol.ChannelPublic)
	priv := e.channel(owner, "priv", protocol.ChannelPrivate)

	// Admins manage and archive public channels they are not in...
	if _, err := e.svc.ArchiveChannel(e.ctx, admin.Actor, pub.ID); err != nil {
		t.Fatalf("admin archives public: %v", err)
	}
	// ...but cannot see a private channel they are not in.
	_, err := e.svc.ArchiveChannel(e.ctx, admin.Actor, priv.ID)
	wantErr(t, err, ErrNotFound)
	_, err = e.svc.ListMessages(e.ctx, admin.Actor, priv.ID, PageOptions{})
	wantErr(t, err, ErrNotFound)

	// Default channels are an admin switch on public channels.
	if _, err := e.svc.SetDefault(e.ctx, admin.Actor, pub.ID, true); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	_, err = e.svc.SetDefault(e.ctx, owner.Actor, pub.ID, false)
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.SetDefault(e.ctx, admin.Actor, priv.ID, true)
	wantErr(t, err, ErrNotFound)
}

func TestListChannels(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	other := e.human("other", protocol.RoleMember)
	guest := e.human("guest", protocol.RoleGuest)
	pub := e.channel(owner, "pub", protocol.ChannelPublic)
	e.channel(owner, "priv", protocol.ChannelPrivate)

	joined, err := e.svc.ListChannels(e.ctx, owner.Actor, ScopeJoined)
	if err != nil || len(joined) != 2 {
		t.Fatalf("joined = %d, %v", len(joined), err)
	}
	dir, err := e.svc.ListChannels(e.ctx, other.Actor, ScopePublic)
	if err != nil || len(dir) != 1 || dir[0].ID != pub.ID {
		t.Fatalf("directory = %+v, %v", dir, err)
	}
	if got, _ := e.svc.ListChannels(e.ctx, other.Actor, ScopeJoined); len(got) != 0 {
		t.Fatalf("other joined %d channels", len(got))
	}
	_, err = e.svc.ListChannels(e.ctx, guest.Actor, ScopePublic)
	wantErr(t, err, ErrForbidden)
	_, err = e.svc.ListChannels(e.ctx, owner.Actor, "everything")
	wantField(t, err, "scope")
}

func TestMembershipPreferences(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	member := e.human("member", protocol.RoleMember)
	stranger := e.human("stranger", protocol.RoleMember)
	ch := e.channel(owner, "prefs", protocol.ChannelPrivate, member)

	until := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	before := e.head()
	m, err := e.svc.UpdateMembership(e.ctx, member.Actor, ch.ID, MembershipUpdate{
		NotifyLevel: ptr(protocol.NotifyAll), AlertMinSeverity: ptr(protocol.SeverityCritical), MutedUntil: Some(until),
	})
	if err != nil {
		t.Fatalf("UpdateMembership: %v", err)
	}
	if m.NotifyLevel != protocol.NotifyAll || m.AlertMinSeverity != protocol.SeverityCritical || m.MutedUntil == nil || *m.MutedUntil != "2030-01-02T03:04:05.000Z" {
		t.Fatalf("membership = %+v", m)
	}
	evs := e.events(before)
	wantTypes(t, evs, "membership.updated")
	if evs[0].User != member.ID || evs[0].Channel != "" {
		t.Fatalf("scope: user %q channel %q", evs[0].User, evs[0].Channel)
	}

	before = e.head()
	if _, err := e.svc.UpdateMembership(e.ctx, member.Actor, ch.ID, MembershipUpdate{NotifyLevel: ptr(protocol.NotifyAll)}); err != nil {
		t.Fatal(err)
	}
	wantTypes(t, e.events(before))
	m, err = e.svc.UpdateMembership(e.ctx, member.Actor, ch.ID, MembershipUpdate{MutedUntil: Clear[time.Time]()})
	if err != nil || m.MutedUntil != nil {
		t.Fatalf("unmute: %+v, %v", m, err)
	}
	_, err = e.svc.UpdateMembership(e.ctx, member.Actor, ch.ID, MembershipUpdate{NotifyLevel: ptr("loud")})
	wantField(t, err, "notify_level")
	_, err = e.svc.UpdateMembership(e.ctx, member.Actor, ch.ID, MembershipUpdate{AlertMinSeverity: ptr("meh")})
	wantField(t, err, "alert_min_severity")
	_, err = e.svc.UpdateMembership(e.ctx, stranger.Actor, ch.ID, MembershipUpdate{NotifyLevel: ptr(protocol.NotifyNone)})
	wantErr(t, err, ErrNotFound)

	pub := e.channel(owner, "open", protocol.ChannelPublic)
	_, err = e.svc.UpdateMembership(e.ctx, stranger.Actor, pub.ID, MembershipUpdate{NotifyLevel: ptr(protocol.NotifyNone)})
	wantErr(t, err, ErrForbidden)
}
