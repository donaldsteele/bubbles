package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// bearer makes a request carry a device token (and no cookie).
func bearer(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Del("Cookie")
		r.Header.Del("Origin")
	}
}

// deviceLogin signs the user in as a tray device from an anonymous client.
func (e *env) deviceLogin(ip, username, password string) *httptest.ResponseRecorder {
	return e.client(ip).do(http.MethodPost, "/api/v1/auth/device-login",
		protocol.DeviceLoginRequest{Username: username, Password: password, DeviceName: "Work laptop"})
}

func (e *env) mustDevice(username string) protocol.DeviceLoginResponse {
	e.t.Helper()
	rec := e.deviceLogin("", username, goodPassword)
	wantStatus(e.t, rec, http.StatusOK, "")
	return decodeBody[protocol.DeviceLoginResponse](e.t, rec)
}

func TestDeviceLoginAndHandoffFlow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	dev := e.mustDevice("admin")
	if !strings.HasPrefix(dev.DeviceToken, "bub_") || dev.HandoffCode == "" || dev.DeviceID == "" {
		t.Fatalf("response = %+v", dev)
	}
	if e.count(`SELECT COUNT(*) FROM device_tokens WHERE token_hash = ?`, auth.HashToken(dev.DeviceToken)) != 1 {
		t.Fatal("the SHA-256 of the device token is not stored")
	}

	web := e.client("")
	rec := web.do(http.MethodGet, "/auth/handoff?code="+dev.HandoffCode, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("handoff = %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	var ck *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			ck = c
		}
	}
	if ck == nil || !ck.Secure || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.MaxAge < 1 {
		t.Fatalf("cookie = %+v", ck)
	}
	wantStatus(t, web.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
	sess := decodeBody[protocol.SessionList](t, web.do(http.MethodGet, "/api/v1/me/sessions", nil))
	handoff := 0
	for _, s := range sess.Items {
		if s.Kind == "handoff" {
			handoff++
		}
	}
	if handoff != 1 {
		t.Fatalf("handoff sessions = %d", handoff)
	}

	// Single use.
	again := e.client("").do(http.MethodGet, "/auth/handoff?code="+dev.HandoffCode, nil)
	if again.Code != http.StatusSeeOther || again.Header().Get("Location") != "/login?error=handoff" || len(again.Result().Cookies()) != 0 {
		t.Fatalf("reused code = %d -> %q cookies %d", again.Code, again.Header().Get("Location"), len(again.Result().Cookies()))
	}
	for _, target := range []string{"/auth/handoff", "/auth/handoff?code=", "/auth/handoff?code=nope"} {
		r := e.client("").do(http.MethodGet, target, nil)
		if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/login?error=handoff" {
			t.Errorf("%s = %d -> %q", target, r.Code, r.Header().Get("Location"))
		}
	}

	// A device asks for a fresh code and it expires after 60 s.
	rec = e.client("").do(http.MethodPost, "/api/v1/auth/handoff-code", nil, bearer(dev.DeviceToken))
	wantStatus(t, rec, http.StatusOK, "")
	code := decodeBody[protocol.HandoffCodeResponse](t, rec).HandoffCode
	e.clock.Advance(61 * time.Second)
	r := e.client("").do(http.MethodGet, "/auth/handoff?code="+code, nil)
	if r.Header().Get("Location") != "/login?error=handoff" {
		t.Fatalf("expired code redirected to %q", r.Header().Get("Location"))
	}
	rec = e.client("").do(http.MethodPost, "/api/v1/auth/handoff-code", nil, bearer(dev.DeviceToken))
	code = decodeBody[protocol.HandoffCodeResponse](t, rec).HandoffCode
	e.clock.Advance(59 * time.Second)
	if r := e.client("").do(http.MethodGet, "/auth/handoff?code="+code, nil); r.Header().Get("Location") != "/" {
		t.Fatalf("code at 59 s redirected to %q", r.Header().Get("Location"))
	}
}

func TestHandoffCodeNeedsADeviceToken(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/auth/handoff-code", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/handoff-code", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/handoff-code", nil, bearer("bub_unknown")), http.StatusUnauthorized, CodeUnauthorized)
}

func TestDeviceTokenReadsOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	dev := e.mustDevice("admin") // an admin: the strictest test of the scope limits
	c := e.client("")
	tok := bearer(dev.DeviceToken)

	rec := c.do(http.MethodGet, "/api/v1/bootstrap", nil, tok)
	wantStatus(t, rec, http.StatusOK, "")
	b := decodeBody[protocol.Bootstrap](t, rec)
	if b.Me.Username != "admin" || len(b.Channels) == 0 {
		t.Fatalf("bootstrap = %+v", b.Me)
	}
	ch := b.Channels[0].ID
	wantStatus(t, c.do(http.MethodGet, "/api/v1/channels/"+ch+"/messages", nil, tok), http.StatusOK, "")
	wantStatus(t, c.do(http.MethodGet, "/api/v1/users/"+b.Me.ID, nil, tok), http.StatusOK, "")

	forbidden := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/overview"},
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPatch, "/api/v1/admin/users/" + b.Me.ID},
		{http.MethodPost, "/api/v1/admin/invites"},
		{http.MethodGet, "/api/v1/admin/audit"},
		{http.MethodPost, "/api/v1/admin/reset"},
		{http.MethodGet, "/api/v1/me"},
		{http.MethodPatch, "/api/v1/me"},
		{http.MethodPost, "/api/v1/me/password"},
		{http.MethodGet, "/api/v1/me/sessions"},
		{http.MethodDelete, "/api/v1/me/sessions/x"},
		{http.MethodGet, "/api/v1/me/devices"},
		{http.MethodPost, "/api/v1/channels/" + ch + "/messages"},
		{http.MethodPut, "/api/v1/messages/x/reactions/x"},
		{http.MethodPost, "/api/v1/channels"},
		{http.MethodPost, "/api/v1/channels/" + ch + "/read"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodPost, "/api/v1/dms"},
		{http.MethodPost, "/api/v1/files"},
		{http.MethodGet, "/api/v1/search?q=a"},
		{http.MethodGet, "/api/v1/apps/self"},
		{http.MethodDelete, "/api/v1/apps/self/token"},
		{http.MethodPut, "/api/v1/apps/self/commands"},
		{http.MethodPost, "/api/v1/alerts"},
		{http.MethodPost, "/api/v1/commands/invoke"},
		{http.MethodGet, "/api/v1/commands"},
		{http.MethodPost, "/api/v1/push/subscriptions"},
		{http.MethodPost, "/api/v1/mcp"},
		{http.MethodDelete, "/api/v1/me/devices/someone-else"},
	}
	for _, tc := range forbidden {
		var body any
		if tc.method != http.MethodGet && tc.method != http.MethodDelete {
			body = map[string]string{}
		}
		rec := c.do(tc.method, tc.path, body, tok)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 (%s)", tc.method, tc.path, rec.Code, truncate(rec.Body.Bytes()))
		}
	}
	if e.count(`SELECT COUNT(*) FROM messages`) != 0 {
		t.Error("a device token posted a message")
	}
}

func TestDeviceRevokeEndsAccess(t *testing.T) {
	e := newEnv(t)
	web := e.setup()
	d1 := e.mustDevice("admin")
	d2 := e.mustDevice("admin")
	c := e.client("")

	list := decodeBody[protocol.DeviceList](t, web.do(http.MethodGet, "/api/v1/me/devices", nil))
	if len(list.Items) != 2 || list.Items[0].Name != "Work laptop" || list.Items[0].Prefix == "" || len(list.Items[0].Scopes) != 3 {
		t.Fatalf("devices = %+v", list)
	}

	// The tray signs itself out; it cannot revoke another device.
	wantStatus(t, c.do(http.MethodDelete, "/api/v1/me/devices/"+d2.DeviceID, nil, bearer(d1.DeviceToken)), http.StatusForbidden, CodeForbidden)
	wantStatus(t, c.do(http.MethodDelete, "/api/v1/me/devices/"+d1.DeviceID, nil, bearer(d1.DeviceToken)), http.StatusNoContent, "")
	wantStatus(t, c.do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(d1.DeviceToken)), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, c.do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(d2.DeviceToken)), http.StatusOK, "")

	// A browser session revokes any device of its user; unknown ids are 404.
	wantStatus(t, web.do(http.MethodDelete, "/api/v1/me/devices/"+d2.DeviceID, nil), http.StatusNoContent, "")
	wantStatus(t, web.do(http.MethodDelete, "/api/v1/me/devices/nope", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, c.do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(d2.DeviceToken)), http.StatusUnauthorized, CodeUnauthorized)
	if list := decodeBody[protocol.DeviceList](t, web.do(http.MethodGet, "/api/v1/me/devices", nil)); len(list.Items) != 0 {
		t.Fatalf("devices after revoke = %d", len(list.Items))
	}
	// Another user's device is invisible.
	other := e.addUser("bob", "192.168.1.90")
	d3 := e.mustDevice("admin")
	wantStatus(t, other.do(http.MethodDelete, "/api/v1/me/devices/"+d3.DeviceID, nil), http.StatusNotFound, CodeNotFound)
}

func TestDeviceLoginShareLockoutWithLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	bad := protocol.LoginRequest{Username: "admin", Password: "wrong password!"}
	c := e.client("")
	for range 3 {
		wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", bad), http.StatusUnauthorized, CodeUnauthorized)
	}
	for range 2 {
		wantStatus(t, e.deviceLogin("", "admin", "wrong password!"), http.StatusUnauthorized, CodeUnauthorized)
	}
	rec := e.deviceLogin("192.168.1.77", "admin", goodPassword)
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusTooManyRequests, CodeRateLimited)
	if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'auth.login_failed'`) != 5 {
		t.Error("device login failures must be audited like login failures")
	}
	e.clock.Advance(16 * time.Minute)
	wantStatus(t, e.deviceLogin("", "admin", goodPassword), http.StatusOK, "")
}

func TestDeviceLoginValidationAndGenericFailure(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/device-login", protocol.DeviceLoginRequest{Username: "admin", Password: goodPassword}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/device-login", `{"username":"admin","password":"x","device_name":"d","extra":1}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.deviceLogin("", "nobody", goodPassword), http.StatusUnauthorized, CodeUnauthorized)
}

func TestDeviceTokenDiesWithPasswordChange(t *testing.T) {
	e := newEnv(t)
	web := e.setup()
	dev := e.mustDevice("admin")
	wantStatus(t, web.do(http.MethodPost, "/api/v1/me/password", protocol.ChangePasswordRequest{Current: goodPassword, New: otherPassword}), http.StatusNoContent, "")
	wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(dev.DeviceToken)), http.StatusUnauthorized, CodeUnauthorized)
}

func TestDeactivationRevokesDeviceTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	e.addUser("bob", "192.168.1.90")
	dev := e.mustDevice("bob")
	wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(dev.DeviceToken)), http.StatusOK, "")
	users := decodeBody[struct {
		Items []protocol.User `json:"items"`
	}](t, admin.do(http.MethodGet, "/api/v1/admin/users", nil))
	var bobID string
	for _, u := range users.Items {
		if u.Username == "bob" {
			bobID = u.ID
		}
	}
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/admin/users/"+bobID, map[string]any{"deactivated": true}), http.StatusOK, "")
	wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/bootstrap", nil, bearer(dev.DeviceToken)), http.StatusUnauthorized, CodeUnauthorized)
}

// GET /auth/handoff?next=: only plain same-origin absolute paths are followed.
func TestHandoffNextIsValidated(t *testing.T) {
	e := newEnv(t)
	e.setup()
	dev := e.mustDevice("admin")
	for _, tc := range []struct{ next, want string }{
		{"/c/01ARZ3NDEKTSV4RRFFQ69G5FAV", "/c/01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"/c/abc?x=1", "/c/abc?x=1"},
		{"", "/"},
		{"c/abc", "/"},
		{"//evil.example/c", "/"},
		{"///evil.example", "/"},
		{"https://evil.example/", "/"},
		{"http:evil.example", "/"},
		{"/\x5cevil.example", "/"},
		{"/c/a\x5cb", "/"},
		{"/c/a\nb", "/"},
		{"/c/a\x00b", "/"},
		{"javascript:alert(1)", "/"},
		{"/" + strings.Repeat("a", 3000), "/"},
	} {
		rec := e.client("").do(http.MethodPost, "/api/v1/auth/handoff-code", nil, bearer(dev.DeviceToken))
		wantStatus(t, rec, http.StatusOK, "")
		code := decodeBody[protocol.HandoffCodeResponse](t, rec).HandoffCode
		target := "/auth/handoff?code=" + code + "&next=" + url.QueryEscape(tc.next)
		r := e.client("").do(http.MethodGet, target, nil)
		if r.Code != http.StatusSeeOther || r.Header().Get("Location") != tc.want {
			t.Errorf("next %q -> %d %q, want %q", tc.next, r.Code, r.Header().Get("Location"), tc.want)
		}
		if len(r.Result().Cookies()) == 0 {
			t.Errorf("next %q: no session cookie set", tc.next)
		}
	}
	// A bad code ignores next and goes to the login page.
	r := e.client("").do(http.MethodGet, "/auth/handoff?code=nope&next=%2Fc%2Fabc", nil)
	if r.Header().Get("Location") != "/login?error=handoff" {
		t.Errorf("bad code with next -> %q", r.Header().Get("Location"))
	}
}
