package httpapi

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// testFingerprintHex is the CA fingerprint of the test server.
var testFingerprintHex = strings.Repeat("0123456789abcdef", 4)

// testEndpoint is how the test server says clients reach it. The fingerprint
// uses the grouped upper-case notation of the certificate manager.
var testEndpoint = apps.Endpoint{Host: "192.168.1.5", Port: 28225, Fingerprint: groupHex(strings.ToUpper(testFingerprintHex))}

func groupHex(h string) string {
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, " ")
}

// bot is an app with an issued token.
type bot struct {
	app   protocol.App
	token string
	info  protocol.AppToken
}

// asBearer makes the request an API client's: a bearer token and no browser
// headers.
func asBearer(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Del("Origin")
	}
}

// do sends a request authenticated by the bot's token from its own address.
func (b bot) do(e *env, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.client("192.168.1.60").do(method, path, body, asBearer(b.token))
}

// newBot creates an app as admin, issues a token with scopes and an optional
// channel allowlist, and adds the bot to the channels.
func (e *env) newBot(admin member, name string, scopes, allowlist []string, channels ...string) bot {
	e.t.Helper()
	rec := admin.do(http.MethodPost, "/api/v1/admin/apps", protocol.CreateAppRequest{Name: name})
	wantStatus(e.t, rec, http.StatusCreated, "")
	app := decodeBody[protocol.App](e.t, rec)
	rec = admin.do(http.MethodPost, "/api/v1/admin/apps/"+app.ID+"/tokens", protocol.CreateAppTokenRequest{Scopes: scopes, ChannelIDs: allowlist})
	wantStatus(e.t, rec, http.StatusCreated, "")
	issued := decodeBody[protocol.IssuedAppToken](e.t, rec)
	for _, ch := range channels {
		wantStatus(e.t, admin.do(http.MethodPut, "/api/v1/channels/"+ch+"/members/"+app.UserID, nil), http.StatusOK, "")
	}
	return bot{app: app, token: issued.Token, info: issued.Info}
}

func TestAppsAdminCreatesAppAndToken(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	rec := admin.do(http.MethodPost, "/api/v1/admin/apps", protocol.CreateAppRequest{Name: "Net Diag", Description: "network checks"})
	wantStatus(t, rec, http.StatusCreated, "")
	app := decodeBody[protocol.App](t, rec)
	if app.Username != "net-diag" || app.Name != "Net Diag" || app.OwnerUserID == nil || *app.OwnerUserID != admin.id || app.Tokens == nil || len(app.Tokens) != 0 {
		t.Fatalf("app = %+v", app)
	}

	general := e.generalID()
	rec = admin.do(http.MethodPost, "/api/v1/admin/apps/"+app.ID+"/tokens", protocol.CreateAppTokenRequest{
		Scopes: []string{"alerts:write", "messages:write"}, ChannelIDs: []string{general},
	})
	wantStatus(t, rec, http.StatusCreated, "")
	issued := decodeBody[protocol.IssuedAppToken](t, rec)
	if !strings.HasPrefix(issued.Token, "bub_") {
		t.Fatalf("token = %q", issued.Token)
	}
	wantConn := "bubbles://192.168.1.5:28225/?token=" + issued.Token + "&fp=sha256:" + testFingerprintHex
	if issued.ConnectionString != wantConn {
		t.Errorf("connection_string = %q, want %q", issued.ConnectionString, wantConn)
	}
	if issued.Info.Prefix != issued.Token[:8] || !slices.Equal(issued.Info.Scopes, []string{"messages:write", "alerts:write"}) || !slices.Equal(issued.Info.ChannelIDs, []string{general}) {
		t.Errorf("info = %+v", issued.Info)
	}

	// The listing shows the token by prefix only; the secret is gone for good.
	rec = admin.do(http.MethodGet, "/api/v1/admin/apps", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), issued.Token) || strings.Contains(rec.Body.String(), issued.Token[4:]) {
		t.Error("the token secret is in the app listing")
	}
	list := decodeBody[protocol.AppList](t, rec)
	if len(list.Items) != 1 || len(list.Items[0].Tokens) != 1 || list.Items[0].Tokens[0].ID != issued.Info.ID {
		t.Errorf("list = %+v", list)
	}

	// PATCH renames, describes and disables.
	name, yes := "Net Diag 2", true
	rec = admin.do(http.MethodPatch, "/api/v1/admin/apps/"+app.ID, protocol.UpdateAppRequest{Name: &name, Disabled: &yes})
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.App](t, rec); got.Name != name || got.DisabledAt == nil {
		t.Errorf("patched app = %+v", got)
	}
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/admin/apps/nope", protocol.UpdateAppRequest{Name: &name}), http.StatusNotFound, CodeNotFound)

	// Every admin action is audited, none with the secret.
	for _, action := range []string{"app.create", "token.create", "app.disable"} {
		if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND actor_kind = 'user' AND actor_id = ?`, action, admin.id); n != 1 {
			t.Errorf("%s audit entries = %d", action, n)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ?`, "%"+issued.Token[4:]+"%"); n != 0 {
		t.Error("the token secret is in the audit log")
	}
}

func TestAppsAdminEndpointsValidateAndAreAdminOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	anon := e.client("")
	app := e.newBot(admin, "bot", []string{"messages:read"}, nil)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/admin/apps", nil},
		{http.MethodPost, "/admin/apps", protocol.CreateAppRequest{Name: "x"}},
		{http.MethodPatch, "/admin/apps/" + app.app.ID, protocol.UpdateAppRequest{}},
		{http.MethodPost, "/admin/apps/" + app.app.ID + "/tokens", protocol.CreateAppTokenRequest{Scopes: []string{"mcp"}}},
		{http.MethodDelete, "/admin/apps/" + app.app.ID + "/tokens/" + app.info.ID, nil},
		{http.MethodGet, "/admin/pairing-requests", nil},
		{http.MethodPost, "/admin/pairing-requests/x/approve", nil},
		{http.MethodPost, "/admin/pairing-requests/x/deny", nil},
	} {
		wantStatus(t, anon.do(tc.method, "/api/v1"+tc.path, tc.body), http.StatusUnauthorized, CodeUnauthorized)
		wantStatus(t, alice.do(tc.method, "/api/v1"+tc.path, tc.body), http.StatusForbidden, CodeForbidden)
		// An app token, even one that exists, is not an admin.
		wantStatus(t, app.do(e, tc.method, "/api/v1"+tc.path, tc.body), http.StatusForbidden, CodeForbidden)
	}

	ed := wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps", protocol.CreateAppRequest{Name: " "}), http.StatusBadRequest, CodeBadRequest)
	if ed.FieldErrors()["name"] == "" {
		t.Errorf("no reason for name: %+v", ed)
	}
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps", protocol.CreateAppRequest{Name: "other", Username: "alice"}), http.StatusBadRequest, CodeBadRequest)
	for _, req := range []protocol.CreateAppTokenRequest{
		{},
		{Scopes: []string{"root"}},
		{Scopes: []string{"mcp"}, ChannelIDs: []string{"nope"}},
	} {
		wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps/"+app.app.ID+"/tokens", req), http.StatusBadRequest, CodeBadRequest)
	}
	past := e.clock.Now().Add(-time.Hour).Format(time.RFC3339)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps/"+app.app.ID+"/tokens", protocol.CreateAppTokenRequest{Scopes: []string{"mcp"}, ExpiresAt: &past}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps/"+app.app.ID+"/tokens", `{"scopes":["mcp"],"expires_at":"soon"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/apps/nope/tokens", protocol.CreateAppTokenRequest{Scopes: []string{"mcp"}}), http.StatusNotFound, CodeNotFound)
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/admin/apps/"+app.app.ID+"/tokens/nope", nil), http.StatusNotFound, CodeNotFound)
}

func TestAppsBearerIsRefusedOnCookieOnlyRoutes(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	b := e.newBot(admin, "bot", protocol.AppScopes, nil)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/me", nil},
		{http.MethodPatch, "/me", protocol.UpdateMeRequest{}},
		{http.MethodPost, "/me/password", nil},
		{http.MethodGet, "/me/sessions", nil},
		{http.MethodDelete, "/me/sessions/x", nil},
		{http.MethodGet, "/setup/status", nil},
		{http.MethodPost, "/setup", nil},
		{http.MethodPost, "/auth/login", nil},
		{http.MethodPost, "/auth/logout", nil},
		{http.MethodGet, "/invites/x", nil},
		{http.MethodPost, "/invites/x/accept", nil},
		{http.MethodPost, "/register", nil},
		{http.MethodPost, "/password-resets/x", nil},
		{http.MethodGet, "/push/vapid-public-key", nil},
		{http.MethodPost, "/push/subscriptions", nil},
		{http.MethodPost, "/pairing/requests", nil},
		{http.MethodGet, "/pairing/requests/x", nil},
		{http.MethodGet, "/admin/apps", nil},
		{http.MethodPost, "/admin/apps", nil},
		{http.MethodPost, "/channels", nil},
		{http.MethodPatch, "/channels/x", nil},
		{http.MethodPost, "/channels/x/join", nil},
		{http.MethodPut, "/channels/x/members/y", nil},
		{http.MethodPost, "/channels/x/read", nil},
	} {
		ed := wantStatus(t, b.do(e, tc.method, "/api/v1"+tc.path, tc.body), http.StatusForbidden, CodeForbidden)
		if !strings.Contains(ed.Message, "not available to app tokens") {
			t.Errorf("%s %s: message = %q", tc.method, tc.path, ed.Message)
		}
	}
	// The same routes answer an invalid token like any anonymous caller.
	wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/me", nil, asBearer("bub_"+strings.Repeat("a", 52))), http.StatusUnauthorized, CodeUnauthorized)
	// A cookie alongside a bearer header is ignored: the bearer decides.
	mixed := admin.do(http.MethodGet, "/api/v1/me", nil, asBearer(b.token))
	wantStatus(t, mixed, http.StatusForbidden, CodeForbidden)
}

func TestAppsScopesAndChannelAllowlistAreEnforced(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	chA := admin.createChannel(t, "chan-a", "public")
	chB := admin.createChannel(t, "chan-b", "public")

	// Restricted to channel A, although the bot is a member of both.
	b := e.newBot(admin, "bot", []string{"messages:read", "messages:write"}, []string{chA.ID}, chA.ID, chB.ID)
	rec := b.do(e, http.MethodPost, "/api/v1/channels/"+chA.ID+"/messages", protocol.CreateMessageRequest{Body: "hello A"})
	wantStatus(t, rec, http.StatusCreated, "")
	helloA := decodeBody[protocol.Message](t, rec)
	if helloA.Author.ID != b.app.UserID || helloA.Author.Kind != "app" {
		t.Errorf("author = %+v", helloA.Author)
	}
	// Channel B is invisible to this token: 404, indistinguishable from a channel that does not exist.
	missing := wantStatus(t, b.do(e, http.MethodPost, "/api/v1/channels/nonexistent/messages", protocol.CreateMessageRequest{Body: "x"}), http.StatusNotFound, CodeNotFound)
	blocked := wantStatus(t, b.do(e, http.MethodPost, "/api/v1/channels/"+chB.ID+"/messages", protocol.CreateMessageRequest{Body: "hello B"}), http.StatusNotFound, CodeNotFound)
	if blocked.Message != missing.Message {
		t.Errorf("restricted channel answers %q, a missing one %q", blocked.Message, missing.Message)
	}
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/channels/"+chB.ID+"/messages", nil), http.StatusNotFound, CodeNotFound)
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE channel_id = ?`, chB.ID); n != 0 {
		t.Errorf("%d messages leaked into channel B", n)
	}
	// Reading channel A works, and the channel list shows A only.
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/channels/"+chA.ID+"/messages", nil), http.StatusOK, "")
	rec = b.do(e, http.MethodGet, "/api/v1/channels?scope=joined", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if list := decodeBody[protocol.ChannelList](t, rec); len(list.Items) != 1 || list.Items[0].ID != chA.ID {
		t.Errorf("channels = %+v", list.Items)
	}

	// Scopes: read-only, write-only and neither.
	readOnly := e.newBot(admin, "reader", []string{"messages:read"}, nil, chA.ID)
	wantStatus(t, readOnly.do(e, http.MethodPost, "/api/v1/channels/"+chA.ID+"/messages", protocol.CreateMessageRequest{Body: "x"}), http.StatusForbidden, CodeForbidden)
	wantStatus(t, readOnly.do(e, http.MethodGet, "/api/v1/channels/"+chA.ID+"/messages", nil), http.StatusOK, "")
	writeOnly := e.newBot(admin, "writer", []string{"messages:write"}, nil, chA.ID)
	wantStatus(t, writeOnly.do(e, http.MethodGet, "/api/v1/channels/"+chA.ID+"/messages", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, writeOnly.do(e, http.MethodPost, "/api/v1/channels/"+chA.ID+"/messages", protocol.CreateMessageRequest{Body: "ok"}), http.StatusCreated, "")
	wantStatus(t, writeOnly.do(e, http.MethodGet, "/api/v1/search?q=ok", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, readOnly.do(e, http.MethodGet, "/api/v1/search?q=hello", nil), http.StatusOK, "")

	// Alerts need alerts:write, uploads need files:write.
	wantStatus(t, writeOnly.do(e, http.MethodPost, "/api/v1/alerts", protocol.AlertInput{Channel: "chan-a", Severity: "info", Title: "t", DedupeKey: "k"}), http.StatusForbidden, CodeForbidden)
	body, ct := multipartBody(t, "file", "a.png", filestest.PlainPNG(8, 8))
	upload := func(b bot) *httptest.ResponseRecorder {
		return e.client("192.168.1.61").do(http.MethodPost, "/api/v1/files", body, asBearer(b.token), func(r *http.Request) { r.Header.Set("Content-Type", ct) })
	}
	wantStatus(t, upload(writeOnly), http.StatusForbidden, CodeForbidden)
	filer := e.newBot(admin, "filer", []string{"files:write", "messages:write"}, nil, chA.ID)
	rec = upload(filer)
	wantStatus(t, rec, http.StatusCreated, "")
	file := decodeBody[protocol.File](t, rec)
	wantStatus(t, filer.do(e, http.MethodPost, "/api/v1/channels/"+chA.ID+"/messages", protocol.CreateMessageRequest{Body: "with file", FileIDs: []string{file.ID}}), http.StatusCreated, "")

	// Routes shared with people still apply the token's scopes.
	wantStatus(t, readOnly.do(e, http.MethodPut, "/api/v1/channels/"+chA.ID+"/pins/"+helloA.ID, nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, readOnly.do(e, http.MethodPut, "/api/v1/messages/"+helloA.ID+"/reactions/%F0%9F%91%8D", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, b.do(e, http.MethodDelete, "/api/v1/messages/nope", nil), http.StatusNotFound, CodeNotFound)
}

func TestAppsRevokedTokenIs401OnNextRequest(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	b := e.newBot(admin, "bot", []string{"messages:read", "messages:write"}, nil, general)

	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/channels/"+general+"/messages", nil), http.StatusOK, "")
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/admin/apps/"+b.app.ID+"/tokens/"+b.info.ID, nil), http.StatusNoContent, "")
	revoked := wantStatus(t, b.do(e, http.MethodGet, "/api/v1/channels/"+general+"/messages", nil), http.StatusUnauthorized, CodeUnauthorized)
	// The answer does not say why: it is the same for a token that never existed.
	unknown := wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil, asBearer("bub_"+strings.Repeat("b", 52))), http.StatusUnauthorized, CodeUnauthorized)
	if revoked.Message != unknown.Message {
		t.Errorf("revoked: %q, unknown: %q", revoked.Message, unknown.Message)
	}
	wantStatus(t, b.do(e, http.MethodPost, "/api/v1/channels/"+general+"/messages", protocol.CreateMessageRequest{Body: "x"}), http.StatusUnauthorized, CodeUnauthorized)

	// The token list shows the revocation; revoking twice is fine.
	rec := admin.do(http.MethodGet, "/api/v1/admin/apps", nil)
	if tok := decodeBody[protocol.AppList](t, rec).Items[0].Tokens[0]; tok.RevokedAt == nil {
		t.Errorf("token = %+v", tok)
	}
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/admin/apps/"+b.app.ID+"/tokens/"+b.info.ID, nil), http.StatusNoContent, "")

	// Expired and disabled-app tokens are refused the same way.
	soon := e.clock.Now().Add(time.Hour).Format(time.RFC3339)
	rec = admin.do(http.MethodPost, "/api/v1/admin/apps/"+b.app.ID+"/tokens", protocol.CreateAppTokenRequest{Scopes: []string{"messages:read"}, ExpiresAt: &soon})
	wantStatus(t, rec, http.StatusCreated, "")
	short := bot{token: decodeBody[protocol.IssuedAppToken](t, rec).Token}
	wantStatus(t, short.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")
	e.clock.Advance(time.Hour)
	wantStatus(t, short.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusUnauthorized, CodeUnauthorized)

	live := e.newBot(admin, "live", []string{"messages:read"}, nil)
	wantStatus(t, live.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")
	yes := true
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/admin/apps/"+live.app.ID, protocol.UpdateAppRequest{Disabled: &yes}), http.StatusOK, "")
	wantStatus(t, live.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestAppsSelf(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	b := e.newBot(admin, "bot", []string{"messages:read", "commands"}, nil)
	rec := b.do(e, http.MethodGet, "/api/v1/apps/self", nil)
	wantStatus(t, rec, http.StatusOK, "")
	self := decodeBody[protocol.AppSelf](t, rec)
	if self.App.ID != b.app.ID || self.Token.ID != b.info.ID || !slices.Equal(self.Token.Scopes, []string{"messages:read", "commands"}) || len(self.App.Tokens) != 0 {
		t.Errorf("self = %+v", self)
	}
	if self.Token.LastUsedAt == nil {
		t.Error("last_used_at not set by the request")
	}
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/apps/self", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, e.client("").do(http.MethodGet, "/api/v1/apps/self", nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestAppsTokenRateLimitIs10PerSecondBurst30(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	b := e.newBot(admin, "bot", []string{"messages:read"}, nil)
	other := e.newBot(admin, "other", []string{"messages:read"}, nil)
	for i := range 30 {
		if rec := b.do(e, http.MethodGet, "/api/v1/apps/self", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i+1, rec.Code)
		}
	}
	rec := b.do(e, http.MethodGet, "/api/v1/apps/self", nil)
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// Per token: another token and the cookie sessions are unaffected.
	wantStatus(t, other.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
	e.clock.Advance(time.Second)
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")
}

func TestAppsOriginCheckAppliesToCookiesOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	b := e.newBot(admin, "bot", []string{"messages:write"}, nil, general)
	post := protocol.CreateMessageRequest{Body: "hi"}
	// A bearer client sends no Origin and needs none; even a stray foreign one is not a CSRF vector.
	wantStatus(t, b.do(e, http.MethodPost, "/api/v1/channels/"+general+"/messages", post), http.StatusCreated, "")
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/channels/"+general+"/messages", post, asBearer(b.token),
		func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }), http.StatusCreated, "")
	// The cookie path is as strict as before.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+general+"/messages", post, func(r *http.Request) { r.Header.Del("Origin") }), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+general+"/messages", post, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+general+"/messages", post), http.StatusCreated, "")
}

func TestAppsCookieRoutesAreUnaffected(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	ch := alice.createChannel(t, "talk", "public")
	alice.post(t, ch.ID, "hello")
	// Browser sessions use every route they used before, with or without apps wired.
	for _, path := range []string{"/me", "/me/sessions", "/bootstrap", "/users", "/channels", "/channels/" + ch.ID + "/messages", "/search?q=hello", "/alerts"} {
		wantStatus(t, alice.do(http.MethodGet, "/api/v1"+path, nil), http.StatusOK, "")
	}
	// A garbage bearer header makes the request anonymous (the cookie is not consulted).
	wantStatus(t, alice.do(http.MethodGet, "/api/v1/me", nil, asBearer("not-a-token")), http.StatusUnauthorized, CodeUnauthorized)
	// Members cannot use the admin API; a cookie never carries scopes.
	wantStatus(t, alice.do(http.MethodGet, "/api/v1/admin/apps", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, alice.do(http.MethodGet, "/api/v1/apps/self", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/admin/apps", nil), http.StatusOK, "")
}

func TestAppsBotCannotSignInWithAPassword(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	b := e.newBot(admin, "bot", []string{"messages:read"}, nil)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: b.app.Username, Password: goodPassword}), http.StatusUnauthorized, CodeUnauthorized)
}

// TestContractBearerMatrix walks every documented operation with a valid app
// token: operations that list bearerAuth must let the token through to their
// handler, every other one must answer 403 "not available to app tokens". It
// keeps docs/openapi.yaml and the routing in step.
func TestContractBearerMatrix(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	b := e.newBot(admin, "bot", protocol.AppScopes, nil)
	doc, _ := loadSpec(t)
	checked := 0
	for path, item := range doc.Paths.Map() {
		if path == "/healthz" || path == "/hooks/{token}" || path == "/auth/handoff" {
			continue
		}
		for method, op := range item.Operations() {
			accepts := false
			if op.Security != nil {
				for _, req := range *op.Security {
					if _, ok := req["bearerAuth"]; ok {
						accepts = true
					}
				}
			}
			target := path
			for _, p := range []string{"{id}", "{code}", "{token_id}", "{user_id}", "{message_id}", "{name}"} {
				target = strings.ReplaceAll(target, p, "x")
			}
			target = strings.ReplaceAll(target, "{emoji}", "%F0%9F%91%8D")
			var body any
			if op.RequestBody != nil {
				body = `{}`
			}
			rec := e.client("192.168.1.62").do(method, "/api/v1"+target, body, asBearer(b.token), func(r *http.Request) {
				r.Header.Set(protocol.PairingHeader, "x")
			})
			refused := rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "not available to app tokens")
			if accepts && refused {
				t.Errorf("%s %s documents bearerAuth but refuses app tokens", method, path)
			}
			if !accepts && !refused {
				t.Errorf("%s %s does not document bearerAuth but answered %d to an app token", method, path, rec.Code)
			}
			checked++
		}
	}
	if checked < 50 {
		t.Errorf("only %d operations checked", checked)
	}
}

// TestContractAppRoutesAreDocumented makes sure none of the T12 operations is
// missing from the spec or lacks its security note.
func TestContractAppRoutesAreDocumented(t *testing.T) {
	doc, _ := loadSpec(t)
	for _, want := range []struct {
		method, path string
		bearer       bool
		cookie       bool
	}{
		{"GET", "/apps/self", true, false},
		{"POST", "/alerts", true, false},
		{"GET", "/alerts", true, true},
		{"POST", "/alerts/resolve", true, false},
		{"POST", "/alerts/{id}/ack", true, true},
		{"POST", "/alerts/{id}/resolve", true, true},
		{"POST", "/pairing/requests", false, false},
		{"GET", "/pairing/requests/{id}", false, false},
		{"GET", "/admin/apps", false, true},
		{"POST", "/admin/apps", false, true},
		{"PATCH", "/admin/apps/{id}", false, true},
		{"POST", "/admin/apps/{id}/tokens", false, true},
		{"DELETE", "/admin/apps/{id}/tokens/{token_id}", false, true},
		{"GET", "/admin/pairing-requests", false, true},
		{"POST", "/admin/pairing-requests/{id}/approve", false, true},
		{"POST", "/admin/pairing-requests/{id}/deny", false, true},
	} {
		item := doc.Paths.Find(want.path)
		if item == nil || item.GetOperation(want.method) == nil {
			t.Errorf("%s %s is not in docs/openapi.yaml", want.method, want.path)
			continue
		}
		op := item.GetOperation(want.method)
		var bearer, cookie bool
		if op.Security != nil {
			for _, req := range *op.Security {
				_, b := req["bearerAuth"]
				_, c := req["cookieAuth"]
				bearer, cookie = bearer || b, cookie || c
			}
		}
		if bearer != want.bearer || cookie != want.cookie {
			t.Errorf("%s %s: security bearer=%v cookie=%v, want bearer=%v cookie=%v", want.method, want.path, bearer, cookie, want.bearer, want.cookie)
		}
	}
	if doc.Components.SecuritySchemes["bearerAuth"] == nil {
		t.Error("bearerAuth security scheme missing")
	}
}
