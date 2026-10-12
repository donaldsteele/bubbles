package chat

import (
	"fmt"
	"slices"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestDMKey(t *testing.T) {
	if got := DMKey([]string{"c", "a", "b", "a", ""}); got != "a,b,c" {
		t.Fatalf("DMKey = %q", got)
	}
	if DMKey([]string{"x", "y"}) != DMKey([]string{"y", "x"}) {
		t.Fatal("DMKey depends on order")
	}
}

func TestStartDMIsIdempotentRegardlessOfOrder(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleAdmin)

	before := e.head()
	dm, created, err := e.svc.StartDM(e.ctx, alice.Actor, []string{bob.ID})
	if err != nil || !created {
		t.Fatalf("StartDM: %+v created=%v err=%v", dm, created, err)
	}
	if dm.Kind != protocol.ChannelDM || dm.Name != nil || dm.MemberCount != 2 {
		t.Fatalf("dm = %+v", dm)
	}
	if want := DMKey([]string{alice.ID, bob.ID}); !slices.Equal(dm.ParticipantIDs, []string{min(alice.ID, bob.ID), max(alice.ID, bob.ID)}) || want == "" {
		t.Fatalf("participants = %v", dm.ParticipantIDs)
	}
	evs := e.events(before)
	wantTypes(t, evs, "channel.created")
	if evs[0].Channel != dm.ID {
		t.Fatalf("event channel = %q", evs[0].Channel)
	}

	again, created, err := e.svc.StartDM(e.ctx, bob.Actor, []string{alice.ID})
	if err != nil || created || again.ID != dm.ID {
		t.Fatalf("reverse order: %+v created=%v err=%v", again, created, err)
	}
	again, created, err = e.svc.StartDM(e.ctx, alice.Actor, []string{bob.ID, alice.ID, bob.ID})
	if err != nil || created || again.ID != dm.ID {
		t.Fatalf("duplicates: %+v created=%v err=%v", again, created, err)
	}
	wantTypes(t, e.events(before), "channel.created")
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE kind = 'dm'`); n != 1 {
		t.Fatalf("%d dm channels, want 1", n)
	}

	// Group DMs: same participants in any order are the same channel.
	g1, created, err := e.svc.StartDM(e.ctx, alice.Actor, []string{bob.ID, carol.ID})
	if err != nil || !created || g1.Kind != protocol.ChannelGroupDM || g1.MemberCount != 3 {
		t.Fatalf("group: %+v created=%v err=%v", g1, created, err)
	}
	g2, created, err := e.svc.StartDM(e.ctx, carol.Actor, []string{bob.ID, alice.ID})
	if err != nil || created || g2.ID != g1.ID {
		t.Fatalf("group reverse: %+v created=%v err=%v", g2, created, err)
	}
	// A different set is a different channel.
	if other, _, err := e.svc.StartDM(e.ctx, alice.Actor, []string{carol.ID}); err != nil || other.ID == dm.ID || other.ID == g1.ID {
		t.Fatalf("other pair: %+v, %v", other, err)
	}
}

func TestNoteToSelf(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	self, created, err := e.svc.StartDM(e.ctx, alice.Actor, nil)
	if err != nil || !created || self.Kind != protocol.ChannelDM || self.MemberCount != 1 || !slices.Equal(self.ParticipantIDs, []string{alice.ID}) {
		t.Fatalf("note to self: %+v created=%v err=%v", self, created, err)
	}
	again, created, err := e.svc.StartDM(e.ctx, alice.Actor, []string{alice.ID})
	if err != nil || created || again.ID != self.ID {
		t.Fatalf("again: %+v created=%v err=%v", again, created, err)
	}
	if m := e.post(alice, self.ID, "remember the milk"); m.ChannelID != self.ID {
		t.Fatalf("message = %+v", m)
	}
}

func TestGroupDMLimits(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	var others []string
	for i := 0; i < 9; i++ {
		others = append(others, e.human(fmt.Sprintf("user%d", i), protocol.RoleMember).ID)
	}
	// 8 others + caller = 9 participants is the maximum.
	g, _, err := e.svc.StartDM(e.ctx, alice.Actor, others[:8])
	if err != nil || g.MemberCount != 9 {
		t.Fatalf("9 participants: %+v, %v", g, err)
	}
	_, _, err = e.svc.StartDM(e.ctx, alice.Actor, others)
	wantField(t, err, "user_ids")
}

func TestDMTargetValidation(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	gone := e.human("gone", protocol.RoleMember)
	e.deactivate(gone)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)

	for name, ids := range map[string][]string{"unknown": {"nope"}, "deactivated": {gone.ID}, "app": {bot.ID}} {
		_, _, err := e.svc.StartDM(e.ctx, alice.Actor, ids)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		wantField(t, err, "user_ids")
	}
	if n := e.count(`SELECT COUNT(*) FROM channels`); n != 0 {
		t.Fatalf("%d channels created by failed requests", n)
	}
}

func TestGuestDMsNeedASharedChannel(t *testing.T) {
	e := newEnv(t)
	owner := e.human("owner", protocol.RoleMember)
	guest := e.human("guest", protocol.RoleGuest)
	buddy := e.human("buddy", protocol.RoleMember)
	stranger := e.human("stranger", protocol.RoleMember)
	e.channel(owner, "project", protocol.ChannelPrivate, guest, buddy)

	if _, _, err := e.svc.StartDM(e.ctx, guest.Actor, []string{buddy.ID}); err != nil {
		t.Fatalf("DM with a channel mate: %v", err)
	}
	_, _, err := e.svc.StartDM(e.ctx, guest.Actor, []string{stranger.ID})
	wantField(t, err, "user_ids")
	_, _, err = e.svc.StartDM(e.ctx, guest.Actor, []string{buddy.ID, stranger.ID})
	wantField(t, err, "user_ids")
	// Members may DM guests they share nothing with.
	if _, _, err := e.svc.StartDM(e.ctx, stranger.Actor, []string{guest.ID}); err != nil {
		t.Fatalf("member DMs a guest: %v", err)
	}
}

func TestAppDMNeedsScope(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bot := e.addUser("bot", protocol.RoleMember, protocol.KindApp)
	act := bot.Actor
	_, _, err := e.svc.StartDM(e.ctx, act, []string{alice.ID})
	wantErr(t, err, ErrForbidden)
	act.Scopes = []string{ScopeDMWrite}
	dm, _, err := e.svc.StartDM(e.ctx, act, []string{alice.ID})
	if err != nil {
		t.Fatalf("with dm:write: %v", err)
	}
	// Posting still needs messages:write.
	_, _, err = e.svc.CreateMessage(e.ctx, act, CreateMessageParams{ChannelID: dm.ID, Body: "hi"})
	wantErr(t, err, ErrForbidden)
	act.Scopes = append(act.Scopes, ScopeMessagesWrite)
	if _, _, err = e.svc.CreateMessage(e.ctx, act, CreateMessageParams{ChannelID: dm.ID, Body: "hi"}); err != nil {
		t.Fatalf("with messages:write: %v", err)
	}
}

func TestDMMembershipIsFixed(t *testing.T) {
	e := newEnv(t)
	alice := e.human("alice", protocol.RoleMember)
	bob := e.human("bob", protocol.RoleMember)
	carol := e.human("carol", protocol.RoleMember)
	admin := e.human("root", protocol.RoleAdmin)
	for _, ids := range [][]string{{bob.ID}, {bob.ID, carol.ID}} {
		dm, _, err := e.svc.StartDM(e.ctx, alice.Actor, ids)
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.svc.AddMember(e.ctx, alice.Actor, dm.ID, admin.ID, "")
		wantErr(t, err, ErrForbidden)
		wantErr(t, e.svc.LeaveChannel(e.ctx, alice.Actor, dm.ID), ErrForbidden)
		wantErr(t, e.svc.RemoveMember(e.ctx, alice.Actor, dm.ID, bob.ID), ErrForbidden)
		_, err = e.svc.UpdateChannel(e.ctx, alice.Actor, dm.ID, UpdateChannelParams{Topic: ptr("x")})
		wantErr(t, err, ErrForbidden)
		_, err = e.svc.ArchiveChannel(e.ctx, alice.Actor, dm.ID)
		wantErr(t, err, ErrForbidden)
		// Outsiders (admins included) cannot even see it.
		_, err = e.svc.JoinChannel(e.ctx, admin.Actor, dm.ID)
		wantErr(t, err, ErrNotFound)
		_, err = e.svc.GetChannel(e.ctx, admin.Actor, dm.ID)
		wantErr(t, err, ErrNotFound)
		_, _, err = e.svc.CreateMessage(e.ctx, admin.Actor, CreateMessageParams{ChannelID: dm.ID, Body: "psst"})
		wantErr(t, err, ErrNotFound)
	}
}
