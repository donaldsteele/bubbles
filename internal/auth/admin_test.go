package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func rolePtr(r Role) *Role { return &r }
func boolPtr(b bool) *bool { return &b }

func TestAdminUpdateUserDeactivationClosesSocketsAndRevokesCredentials(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("boss", RoleAdmin)
	target := e.addUser("ana", RoleMember)
	e.mustLogin("ana")
	e.mustLogin("ana")
	// A device token and an app token owned by the user's credentials.
	e.execSQL(`INSERT INTO device_tokens (id, user_id, name, token_hash, prefix, scopes, created_at) VALUES ('d1', ?, 'tray', x'01', 'dev_', '[]', 0)`, target.ID)
	e.execSQL(`INSERT INTO users (id, username, kind, role, created_at) VALUES ('botuser', 'bot', 'app', 'member', 0)`)
	e.execSQL(`INSERT INTO apps (id, user_id, name, created_at) VALUES ('app1', 'botuser', 'bot', 0)`)
	e.execSQL(`INSERT INTO app_tokens (id, app_id, token_hash, prefix, scopes, created_at) VALUES ('t1', 'app1', x'02', 'bub_', '[]', 0)`)

	hub := events.NewHub(events.HubOptions{DB: e.db, Now: e.clock.Now})
	t.Cleanup(hub.Close)
	events.SetPublisher(e.db, hub)
	closed := make(chan int, 4)
	hub.Subscribe(events.Viewer{UserID: target.ID, Role: protocol.RoleMember, Kind: protocol.KindHuman}, "sess-a",
		func(code int, _ string) { closed <- code })
	bystander := make(chan int, 1)
	hub.Subscribe(events.Viewer{UserID: admin.ID, Role: protocol.RoleAdmin, Kind: protocol.KindHuman}, "sess-b",
		func(code int, _ string) { bystander <- code })

	got, err := e.svc.AdminUpdateUser(e.ctx, audit.User(admin.ID, "10.0.0.1"), target.ID, AdminUserPatch{Deactivated: boolPtr(true)})
	if err != nil {
		t.Fatal(err)
	}
	if got.DeactivatedAt == nil {
		t.Fatal("user is not deactivated")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the deactivated user's socket stayed open")
	}
	select {
	case <-bystander:
		t.Fatal("another user's socket was closed")
	default:
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, target.ID); n != 0 {
		t.Errorf("sessions left: %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM device_tokens WHERE revoked_at IS NULL`); n != 0 {
		t.Errorf("live device tokens left: %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'user.updated' AND payload LIKE '%"deactivated":true%'`); n != 1 {
		t.Errorf("user.updated deactivated events = %d", n)
	}
	if e.auditCount(audit.ActionUserUpdate) != 1 {
		t.Errorf("audit rows = %d", e.auditCount(audit.ActionUserUpdate))
	}
	if _, err := e.login("ana", goodPassword); err == nil {
		t.Error("deactivated user can sign in")
	}
	// Reactivation works and is a change again.
	if _, err := e.svc.AdminUpdateUser(e.ctx, audit.User(admin.ID, ""), target.ID, AdminUserPatch{Deactivated: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.login("ana", goodPassword); err != nil {
		t.Errorf("reactivated user cannot sign in: %v", err)
	}
}

func TestAdminUpdateUserGuards(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("boss", RoleAdmin)
	other := e.addUser("second", RoleAdmin)
	member := e.addUser("ana", RoleMember)
	actor := audit.User(admin.ID, "")

	tests := []struct {
		name  string
		actor audit.Actor
		id    string
		patch AdminUserPatch
		want  error
	}{
		{"self demote", actor, admin.ID, AdminUserPatch{Role: rolePtr(RoleMember)}, ErrSelfLockout},
		{"self deactivate", actor, admin.ID, AdminUserPatch{Deactivated: boolPtr(true)}, ErrSelfLockout},
		{"unknown user", actor, "nope", AdminUserPatch{Role: rolePtr(RoleGuest)}, ErrNotFound},
		{"promote member", actor, member.ID, AdminUserPatch{Role: rolePtr(RoleAdmin)}, nil},
		{"demote other admin", actor, other.ID, AdminUserPatch{Role: rolePtr(RoleMember)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := e.svc.AdminUpdateUser(e.ctx, tt.actor, tt.id, tt.patch)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	// ana is an admin now and boss the only other one: demoting boss as ana
	// leaves ana, but a deactivated admin cannot be the one who remains.
	if _, err := e.svc.AdminUpdateUser(e.ctx, audit.User(member.ID, ""), admin.ID, AdminUserPatch{Deactivated: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AdminUpdateUser(e.ctx, audit.Console, member.ID, AdminUserPatch{Role: rolePtr(RoleGuest)}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin: err = %v", err)
	}
	var verr *ValidationError
	if _, err := e.svc.AdminUpdateUser(e.ctx, actor, member.ID, AdminUserPatch{Role: rolePtr("root")}); !errors.As(err, &verr) {
		t.Errorf("bad role: err = %v", err)
	}
	if _, err := e.svc.AdminUpdateUser(e.ctx, actor, member.ID, AdminUserPatch{}); !errors.As(err, &verr) {
		t.Errorf("empty patch: err = %v", err)
	}
}

func TestRevokeAllSessions(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("boss", RoleAdmin)
	target := e.addUser("ana", RoleMember)
	e.mustLogin("ana")
	if err := e.svc.RevokeAllSessions(e.ctx, audit.User(admin.ID, ""), target.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`, target.ID); n != 0 {
		t.Errorf("sessions left: %d", n)
	}
	if err := e.svc.RevokeAllSessions(e.ctx, audit.Console, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

// Revoking all of a user's sessions also revokes their device tokens and
// appends a per-device session.revoked event, which closes the sockets.
func TestRevokeAllSessionsAlsoRevokesDevices(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("boss", RoleAdmin)
	target := e.addUser("ana", RoleMember)
	other := e.addUser("bob", RoleMember)
	d := e.mustDeviceLogin("ana")
	od := e.mustDeviceLogin("bob")
	if e.deviceAuth(d.Token) == nil {
		t.Fatal("device token does not authenticate before the revoke")
	}
	before := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ?`, target.ID)
	if err := e.svc.RevokeAllSessions(e.ctx, audit.User(admin.ID, ""), target.ID); err != nil {
		t.Fatal(err)
	}
	if e.deviceAuth(d.Token) != nil {
		t.Fatal("device token survived RevokeAllSessions")
	}
	if n := e.count(`SELECT COUNT(*) FROM device_tokens WHERE user_id = ? AND revoked_at IS NULL`, target.ID); n != 0 {
		t.Errorf("%d active device tokens left", n)
	}
	// One event for the sessions, one naming the device: the gateway closes
	// the device socket on the latter.
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ?`, target.ID); n != before+2 {
		t.Errorf("session.revoked events = %d, want %d", n, before+2)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND payload LIKE '%token_id%'`); n != 1 {
		t.Errorf("device close events = %d, want 1", n)
	}
	if e.deviceAuth(od.Token) == nil {
		t.Error("another user's device token was revoked")
	}
	_ = other
}
