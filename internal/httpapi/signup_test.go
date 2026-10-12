package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (e *env) status() protocol.ServerStatus {
	e.t.Helper()
	rec := e.client("").do(http.MethodGet, "/api/v1/status", nil)
	wantStatus(e.t, rec, http.StatusOK, "")
	return decodeBody[protocol.ServerStatus](e.t, rec)
}

func TestStatusReportsSignupWindow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	closes := protocol.FormatTime(e.clock.Now().Add(24 * time.Hour).UnixMilli())
	st := e.status()
	if !st.RegistrationOpen || st.RegistrationClosesAt == nil || *st.RegistrationClosesAt != closes {
		t.Fatalf("open status = %+v", st)
	}
	e.clock.Advance(24*time.Hour - time.Millisecond)
	if !e.status().RegistrationOpen {
		t.Error("closed a millisecond early")
	}
	e.clock.Advance(time.Millisecond)
	if st := e.status(); st.RegistrationOpen || st.RegistrationClosesAt != nil {
		t.Errorf("status after the deadline = %+v", st)
	}
}

func TestRegisterAfterDeadlineAndLegacyFields(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Name: "Early", Password: goodPassword}), http.StatusCreated, "")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/register", `{"name":"Legacy","username":"legacy","password":"`+goodPassword+`"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/register", `{"username":"legacy","display_name":"L","password":"`+goodPassword+`"}`), http.StatusBadRequest, CodeBadRequest)
	e.clock.Advance(25 * time.Hour)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Name: "Late", Password: goodPassword}), http.StatusForbidden, CodeForbidden)
}

func TestDeviceRegister(t *testing.T) {
	e := newEnv(t)
	e.setup()
	post := func(ip, name string) *httptest.ResponseRecorder {
		return e.client(ip).do(http.MethodPost, "/api/v1/auth/device-register",
			protocol.DeviceRegisterRequest{Name: name, Password: goodPassword, DeviceName: "Work laptop"})
	}
	rec := post("192.168.1.60", "Ada Lovelace")
	wantStatus(t, rec, http.StatusCreated, "")
	dev := decodeBody[protocol.DeviceLoginResponse](t, rec)
	if dev.DeviceToken == "" || dev.HandoffCode == "" {
		t.Fatalf("response = %+v", dev)
	}
	me := e.client("").do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(dev.DeviceToken))
	wantStatus(t, me, http.StatusOK, "")
	if u := decodeBody[protocol.Bootstrap](t, me).Me; u.DisplayName != "Ada Lovelace" || u.Role != "member" {
		t.Errorf("me = %+v", u)
	}
	wantStatus(t, post("192.168.1.61", "ada lovelace"), http.StatusConflict, CodeConflict)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/device-register", protocol.DeviceRegisterRequest{Name: "x"}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/device-register", `{"name":"x","password":"p","device_name":"d","username":"u"}`), http.StatusBadRequest, CodeBadRequest)

	// The limit is per IP and shared with /register.
	for i := 0; i < auth.SignupIPMaxAttempts; i++ {
		post("192.168.1.62", fmt.Sprintf("Person %d", i))
	}
	rec = post("192.168.1.62", "One Too Many")
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	wantStatus(t, e.client("192.168.1.62").do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Name: "Via Register", Password: goodPassword}), http.StatusTooManyRequests, CodeRateLimited)
	wantStatus(t, post("192.168.1.63", "Other Host"), http.StatusCreated, "")

	e.clock.Advance(25 * time.Hour)
	wantStatus(t, post("192.168.1.64", "Latecomer"), http.StatusForbidden, CodeForbidden)
}

func TestAdminRegistrationReopenAndClose(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.clock.Advance(48 * time.Hour)
	if e.status().RegistrationOpen {
		t.Fatal("window did not close")
	}
	rec := admin.do(http.MethodPost, adminAPIPath+"/registration", protocol.RegistrationActionRequest{Action: "reopen"})
	wantStatus(t, rec, http.StatusOK, "")
	s := decodeBody[protocol.AdminSettings](t, rec)
	want := protocol.FormatTime(e.clock.Now().Add(24 * time.Hour).UnixMilli())
	if !s.OpenRegistration || !s.RegistrationOpen || s.OpenRegistrationUntil == nil || *s.OpenRegistrationUntil != want {
		t.Fatalf("after reopen = %+v, want until %s", s, want)
	}
	if !e.status().RegistrationOpen {
		t.Error("status says closed after reopen")
	}
	rec = admin.do(http.MethodPost, adminAPIPath+"/registration", protocol.RegistrationActionRequest{Action: "close"})
	wantStatus(t, rec, http.StatusOK, "")
	s = decodeBody[protocol.AdminSettings](t, rec)
	if s.OpenRegistration || s.RegistrationOpen || s.OpenRegistrationUntil != nil {
		t.Fatalf("after close = %+v", s)
	}
	if e.status().RegistrationOpen {
		t.Error("status says open after close")
	}
	if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'registration.open'`) != 1 ||
		e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'registration.close'`) != 1 {
		t.Error("reopen and close are not both audited")
	}
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/registration", protocol.RegistrationActionRequest{Action: "toggle"}), http.StatusBadRequest, CodeBadRequest)
	member := e.addUser("bob", "192.168.1.31")
	wantStatus(t, member.do(http.MethodPost, adminAPIPath+"/registration", protocol.RegistrationActionRequest{Action: "reopen"}), http.StatusForbidden, CodeForbidden)
}

func TestPatchSettingsSignupDeadline(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	patch := func(body string) protocol.AdminSettings {
		t.Helper()
		rec := admin.do(http.MethodPatch, adminAPIPath+"/settings", body)
		wantStatus(t, rec, http.StatusOK, "")
		return decodeBody[protocol.AdminSettings](t, rec)
	}
	at := func(d time.Duration) string { return protocol.FormatTime(e.clock.Now().Add(d).UnixMilli()) }

	s := patch(`{"open_registration":false}`)
	if s.OpenRegistrationUntil != nil || s.RegistrationOpen {
		t.Fatalf("off = %+v", s)
	}
	e.clock.Advance(time.Hour)
	if s = patch(`{"open_registration":true}`); s.OpenRegistrationUntil == nil || *s.OpenRegistrationUntil != at(24*time.Hour) || !s.RegistrationOpen {
		t.Fatalf("on = %+v", s)
	}
	e.clock.Advance(time.Hour)
	if s = patch(`{"registration_window_hours":3}`); s.RegistrationWindowH != 3 || s.OpenRegistrationUntil == nil || *s.OpenRegistrationUntil != at(3*time.Hour) {
		t.Fatalf("window 3 = %+v", s)
	}
	if s = patch(`{"registration_window_hours":0}`); s.OpenRegistrationUntil != nil || !s.RegistrationOpen {
		t.Fatalf("window 0 = %+v", s)
	}
	er := wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"open_registration_until":null}`), http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["open_registration_until"] == "" {
		t.Errorf("fields = %v", er.FieldErrors())
	}
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"registration_window_hours":9000}`), http.StatusBadRequest, CodeBadRequest)
}

func TestAdminCreateUserEndpoint(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	rec := admin.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "Bob Builder", Password: goodPassword})
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.User](t, rec); u.Username != "bob-builder" || u.Role != "member" || u.DisplayName != "Bob Builder" {
		t.Errorf("user = %+v", u)
	}
	rec = admin.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "Gus Guest", Password: goodPassword, Role: "guest"})
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.User](t, rec); u.Role != "guest" {
		t.Errorf("role = %q", u.Role)
	}
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "Bob Builder", Password: goodPassword}), http.StatusOK, "")
	er := wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "Eve", Password: goodPassword, Role: "owner"}), http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["role"] == "" {
		t.Errorf("fields = %v", er.FieldErrors())
	}
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "bob builder", Password: goodPassword}), http.StatusConflict, CodeConflict)
	if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'user.create'`) != 2 {
		t.Error("user.create not audited twice")
	}
	// Works while sign-up is closed; a member may not.
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/registration", protocol.RegistrationActionRequest{Action: "close"}), http.StatusOK, "")
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "Carol", Password: goodPassword}), http.StatusCreated, "")
	member := e.addUser("dave", "192.168.1.31")
	wantStatus(t, member.do(http.MethodPost, adminAPIPath+"/users", protocol.AdminCreateUserRequest{Name: "Mallory", Password: goodPassword}), http.StatusForbidden, CodeForbidden)
}
