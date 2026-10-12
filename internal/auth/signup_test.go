package auth

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/settings"
)

func TestBootstrapStartsSignupWindow(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.BootstrapAdmin(e.ctx); err != nil {
		t.Fatal(err)
	}
	want := e.clock.Now().Add(24 * time.Hour)
	if got := e.settings.RegistrationClosesAt(); got == nil || !got.Equal(want) {
		t.Fatalf("closes at %v, want %v", got, want)
	}
}

func TestBootstrapWindowZeroHasNoDeadline(t *testing.T) {
	e := newEnv(t)
	if err := e.settings.Set(e.ctx, audit.System, settings.KeyRegistrationWindowH, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BootstrapAdmin(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.settings.RegistrationClosesAt() != nil {
		t.Fatal("window 0 set a deadline")
	}
	e.clock.Advance(1000 * time.Hour)
	if !e.settings.RegistrationOpen(e.clock.Now()) {
		t.Fatal("window 0 closed by itself")
	}
}

func TestBootstrapKeepsSignupOff(t *testing.T) {
	e := newEnv(t)
	if err := e.settings.Set(e.ctx, audit.System, settings.KeyOpenRegistration, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.BootstrapAdmin(e.ctx); err != nil {
		t.Fatal(err)
	}
	if e.settings.Values().OpenRegistrationEnds != nil || e.settings.RegistrationOpen(e.clock.Now()) {
		t.Fatal("bootstrap changed a closed sign-up")
	}
}

func TestRegisterClosesWithTheWindow(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.BootstrapAdmin(e.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.registerName("Early Bird"); err != nil {
		t.Fatalf("inside the window: %v", err)
	}
	e.clock.Advance(24*time.Hour - time.Millisecond)
	if _, err := e.registerName("Last Minute"); err != nil {
		t.Fatalf("just before the deadline: %v", err)
	}
	e.clock.Advance(time.Millisecond)
	if _, err := e.registerName("Latecomer"); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("at the deadline: %v", err)
	}
	// A restart (new settings store over the same database) stays closed.
	if settings.New(e.db).RegistrationOpen(e.clock.Now()) {
		t.Fatal("restart reopened sign-up")
	}
	// Reopening starts a new window.
	if err := e.settings.ReopenRegistration(e.ctx, audit.System, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.registerName("Second Chance"); err != nil {
		t.Fatalf("after reopen: %v", err)
	}
}

func TestDeviceRegister(t *testing.T) {
	e := newEnv(t)
	in := DeviceRegisterInput{Name: "Ada Lovelace", Password: goodPassword, DeviceName: "Work laptop", UserAgent: "tray", IP: "192.168.1.20"}
	res, err := e.svc.DeviceRegister(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.HandoffCode == "" || res.DeviceID == "" || res.User.Role != RoleMember || res.User.Username != "ada-lovelace" {
		t.Fatalf("result = %+v", res)
	}
	p := e.deviceAuth(res.Token)
	if p == nil || p.Via != ViaDevice || p.User.ID != res.User.ID {
		t.Fatalf("principal = %+v", p)
	}
	if e.auditCount(audit.ActionRegister) != 1 || e.auditCount(audit.ActionDeviceLogin) != 1 {
		t.Error("register and device login not both audited")
	}
	if _, err := e.svc.DeviceRegister(e.ctx, in); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	bad := in
	bad.Name, bad.DeviceName = "Grace", " "
	var ve *ValidationError
	if _, err := e.svc.DeviceRegister(e.ctx, bad); !errors.As(err, &ve) || ve.Fields["device_name"] == "" {
		t.Errorf("empty device name: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE display_name = 'Grace'`); n != 0 {
		t.Error("a rejected device registration created the user")
	}
	if err := e.settings.CloseRegistration(e.ctx, audit.System); err != nil {
		t.Fatal(err)
	}
	in.Name = "Grace Hopper"
	if _, err := e.svc.DeviceRegister(e.ctx, in); !errors.Is(err, ErrRegistrationClosed) {
		t.Errorf("closed: %v", err)
	}
}

func TestSignupLimiterPerIP(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < SignupIPMaxAttempts; i++ {
		// Alternate the two endpoints: they share the limit. Failures count too.
		if i%2 == 0 {
			_, _ = e.svc.Register(e.ctx, RegisterInput{Name: fmt.Sprintf("Person %d", i), Password: goodPassword, IP: "192.168.1.30"})
		} else {
			_, _ = e.svc.DeviceRegister(e.ctx, DeviceRegisterInput{Name: "", Password: goodPassword, DeviceName: "d", IP: "192.168.1.30"})
		}
	}
	var rl *RateLimitedError
	if _, err := e.registerNameFrom("One Too Many", "192.168.1.30"); !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("21st attempt: %v", err)
	}
	if _, err := e.registerNameFrom("Other Host", "192.168.1.31"); err != nil {
		t.Fatalf("another IP: %v", err)
	}
	e.clock.Advance(time.Hour + time.Second)
	if _, err := e.registerNameFrom("One Too Many", "192.168.1.30"); err != nil {
		t.Fatalf("after the hour: %v", err)
	}
}

func (e *testEnv) registerNameFrom(name, ip string) (*SessionResult, error) {
	return e.svc.Register(e.ctx, RegisterInput{Name: name, Password: goodPassword, IP: ip})
}

func TestAdminCreateUser(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("root", RoleAdmin)
	actor := audit.User(admin.ID, "192.168.1.2")
	u, err := e.svc.AdminCreateUser(e.ctx, actor, AdminCreateUserInput{Name: "Bob Builder", Password: goodPassword})
	if err != nil || u.Role != RoleMember || u.Username != "bob-builder" {
		t.Fatalf("default role: %+v, %v", u, err)
	}
	if _, err := e.login("bob-builder", goodPassword); err != nil {
		t.Fatalf("new user cannot sign in: %v", err)
	}
	for _, role := range []Role{RoleAdmin, RoleGuest} {
		got, err := e.svc.AdminCreateUser(e.ctx, actor, AdminCreateUserInput{Name: "Person " + string(role), Password: goodPassword, Role: role})
		if err != nil || got.Role != role {
			t.Errorf("role %s: %+v, %v", role, got, err)
		}
	}
	if _, err := e.svc.AdminCreateUser(e.ctx, actor, AdminCreateUserInput{Name: "bob builder", Password: goodPassword}); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	var ve *ValidationError
	if _, err := e.svc.AdminCreateUser(e.ctx, actor, AdminCreateUserInput{Name: "Eve", Password: goodPassword, Role: "owner"}); !errors.As(err, &ve) || ve.Fields["role"] == "" {
		t.Errorf("bad role: %v", err)
	}
	if e.auditCount(audit.ActionUserCreate) != 3 {
		t.Errorf("user.create rows = %d, want 3", e.auditCount(audit.ActionUserCreate))
	}
	// Not gated by the sign-up window or the sign-up limit.
	if err := e.settings.CloseRegistration(e.ctx, audit.System); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < SignupIPMaxAttempts+1; i++ {
		_, _ = e.registerNameFrom("x", "192.168.1.40")
	}
	if _, err := e.svc.AdminCreateUser(e.ctx, actor, AdminCreateUserInput{Name: "Carol", Password: goodPassword}); err != nil {
		t.Errorf("while closed: %v", err)
	}
}
