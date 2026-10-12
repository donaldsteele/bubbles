package chat

import (
	"slices"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

var cheapParams = auth.Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}

const goodPassword = "correct horse battery"

// TestGeneralAtBootstrapAndAutoJoin wires the chat hooks into a real auth service,
// the way internal/app does, and checks #general and the auto-join rules for
// every way an account is created (bootstrap, invite, register).
func TestGeneralAtBootstrapAndAutoJoin(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	svc, err := auth.New(auth.Options{
		DB: e.db, Settings: e.st, DataDir: dir, Now: e.clock.Now, Params: cheapParams,
		OnBootstrap: e.svc.OnBootstrapTx, OnUserCreated: e.svc.OnUserCreatedTx,
	})
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	pw, err := svc.BootstrapAdmin(e.ctx)
	if err != nil || pw == "" {
		t.Fatalf("BootstrapAdmin: %q, %v", pw, err)
	}
	res, err := svc.Login(e.ctx, auth.LoginInput{Username: auth.BootstrapAdminUsername, Password: pw})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	adminID := res.User.ID

	// #general: public, default, created by and owned by the admin.
	general, err := e.svc.ListChannels(e.ctx, Actor{ID: adminID, Role: protocol.RoleAdmin}, ScopeJoined)
	if err != nil || len(general) != 1 {
		t.Fatalf("admin channels = %+v, %v", general, err)
	}
	g := general[0]
	if g.Name == nil || *g.Name != GeneralName || g.Kind != protocol.ChannelPublic || !g.IsDefault ||
		g.CreatedBy == nil || *g.CreatedBy != adminID || g.MemberCount != 1 {
		t.Fatalf("#general = %+v", g)
	}
	if ms := mustMembers(t, e, user{Actor: Actor{ID: adminID, Role: protocol.RoleAdmin}}, g.ID); len(ms) != 1 || ms[0].Role != protocol.MemberOwner {
		t.Fatalf("members = %+v", ms)
	}
	bootstrapEvents := e.events(0)
	if !slices.ContainsFunc(bootstrapEvents, func(l logged) bool { return l.Type == "channel.created" && l.Channel == g.ID }) {
		t.Fatalf("bootstrap events = %v, want channel.created for #general", types(bootstrapEvents))
	}

	adminActor := audit.User(adminID, "")
	invite := func(role auth.Role, username string) string {
		_, code, err := svc.CreateInvite(e.ctx, adminActor, auth.CreateInviteInput{Role: role, ExpiresIn: time.Hour, MaxUses: 1})
		if err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
		r, err := svc.AcceptInvite(e.ctx, auth.AcceptInviteInput{Code: code, Username: username, Password: goodPassword, DisplayName: username})
		if err != nil {
			t.Fatalf("AcceptInvite(%s): %v", username, err)
		}
		return r.User.ID
	}
	memberID := invite(auth.RoleMember, "newmember")
	guestID := invite(auth.RoleGuest, "newguest")
	adminTwoID := invite(auth.RoleAdmin, "second")

	if err := e.st.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		t.Fatal(err)
	}
	reg, err := svc.Register(e.ctx, auth.RegisterInput{Name: "Self", Password: goodPassword})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	inGeneral := func(id string) bool {
		return e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, g.ID, id) == 1
	}
	for name, id := range map[string]string{"invited member": memberID, "invited admin": adminTwoID, "registered member": reg.User.ID} {
		if !inGeneral(id) {
			t.Errorf("%s was not joined to #general", name)
		}
	}
	if inGeneral(guestID) {
		t.Error("a guest must not be auto-joined to #general")
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ?`, g.ID); n != 4 {
		t.Errorf("#general has %d members, want 4", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'channel.member_joined'`); n != 3 {
		t.Errorf("%d member_joined events, want 3", n)
	}

	// The new member lands with the right defaults and no unread history.
	b, err := e.svc.Bootstrap(e.ctx, Actor{ID: memberID})
	if err != nil || len(b.Memberships) != 1 || b.Memberships[0].NotifyLevel != protocol.NotifyMentions ||
		b.Memberships[0].Role != protocol.MemberMember || b.Unread[0].Unread != 0 {
		t.Fatalf("bootstrap = %+v, %v", b, err)
	}
	gb, err := e.svc.Bootstrap(e.ctx, Actor{ID: guestID})
	if err != nil || len(gb.Channels) != 0 || len(gb.Memberships) != 0 {
		t.Fatalf("guest bootstrap = %+v, %v", gb, err)
	}

	// Archived default channels are skipped; a second default is joined too.
	second, err := e.svc.CreateChannel(e.ctx, Actor{ID: adminID, Role: protocol.RoleAdmin}, CreateChannelParams{Name: "random", Kind: protocol.ChannelPublic})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetDefault(e.ctx, Actor{ID: adminID, Role: protocol.RoleAdmin}, second.ID, true); err != nil {
		t.Fatal(err)
	}
	lateID := invite(auth.RoleMember, "latecomer")
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE user_id = ?`, lateID); n != 2 {
		t.Errorf("latecomer joined %d default channels, want 2", n)
	}
}

// TestOnBootstrapIsIdempotent checks a repeated bootstrap hook keeps a single #general.
func TestOnBootstrapIsIdempotent(t *testing.T) {
	e := newEnv(t)
	admin := e.human("root", protocol.RoleAdmin)
	for i := 0; i < 2; i++ {
		err := e.db.Do(e.ctx, func(tx *store.Tx) error { return e.svc.OnBootstrapTx(e.ctx, tx, admin.ID) })
		if err != nil {
			t.Fatalf("OnBootstrapTx #%d: %v", i, err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE name = 'general'`); n != 1 {
		t.Fatalf("%d #general channels", n)
	}
}
