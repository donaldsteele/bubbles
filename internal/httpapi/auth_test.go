package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/alerts"
	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/files"
	"github.com/donaldsteele/bubbles/internal/hooks"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/search"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const (
	goodPassword  = "correct horse battery"
	otherPassword = "a brand new passphrase"
	cookieName    = "__Host-bubbles_session"
)

var cheapParams = auth.Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}

// fakeClock is shared by the store, the auth service and its rate limiters.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// env is a full handler stack over a temp database.
type env struct {
	t       *testing.T
	ctx     context.Context
	db      *store.DB
	st      *settings.Store
	svc     *auth.Service
	chat    *chat.Service
	files   *files.Service
	push    *push.Service
	apps    *apps.Service
	alerts  *alerts.Service
	hooks   *hooks.Service
	free    *atomic.Uint64 // free disk space reported to the files service
	clock   *fakeClock
	dataDir string
	reset   *fakeResetter
	h       http.Handler
	raw     http.Handler // h without the contract recorder (streams, real servers)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvLogged(t, nil)
}

// newEnvLogged is newEnv with the server's logger set (nil discards).
func newEnvLogged(t *testing.T, logger *slog.Logger) *env {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := settings.New(db)
	cs := chat.New(db, st)
	free := new(atomic.Uint64)
	free.Store(1 << 40)
	fs, err := files.New(files.Options{
		DB: db, Settings: st, DataDir: dir, FreeSpace: func(string) (uint64, error) { return free.Load(), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	cs.SetFileAttacher(fs)
	ps, err := push.New(push.Options{DB: db, Settings: st, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ps.Close)
	svc, err := auth.New(auth.Options{
		DB: db, Settings: st, DataDir: dir, Now: clock.Now, Params: cheapParams,
		OnSetup: cs.OnSetupTx, OnUserCreated: cs.OnUserCreatedTx,
	})
	if err != nil {
		t.Fatal(err)
	}
	as := apps.New(apps.Options{DB: db, Chat: cs, Now: clock.Now})
	al := alerts.New(alerts.Options{DB: db, Chat: cs, Settings: st})
	hk := hooks.New(hooks.Options{DB: db, Chat: cs, Apps: as, Alerts: al, Now: clock.Now})
	rs := newFakeResetter(dir, db)
	raw := New(Deps{
		Version: "v9.9.9", SPA: testSPA, AllowedCIDRs: st.AllowedCIDRs, Auth: svc, Chat: cs, Files: fs,
		Search: search.New(db, cs, time.UTC), Push: ps, Apps: as, Alerts: al, Hooks: hk,
		Endpoint: func() apps.Endpoint { return testEndpoint }, Now: clock.Now, Reset: rs,
		Admin: fakeAdminHost{start: clock.Now()}, DB: db, Logger: logger,
	})
	h := contractHandler(t, raw)
	return &env{t: t, ctx: context.Background(), db: db, st: st, svc: svc, chat: cs, files: fs, push: ps, apps: as, alerts: al, hooks: hk, free: free, clock: clock, dataDir: dir, reset: rs, h: h, raw: raw}
}

// client is a browser-like caller with its own cookie jar and source IP.
type client struct {
	t       *testing.T
	h       http.Handler
	ip      string
	cookies map[string]string
}

func (e *env) client(ip string) *client {
	if ip == "" {
		ip = "192.168.1.50"
	}
	return &client{t: e.t, h: e.h, ip: ip, cookies: map[string]string{}}
}

// do sends a same-origin request; mods may override any header.
func (c *client) do(method, path string, body any, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	c.t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	case []byte:
		rdr = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			c.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = c.ip + ":50000"
	req.Header.Set("User-Agent", "test-agent")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", "https://"+req.Host)
	}
	for name, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: v})
	}
	for _, m := range mods {
		m(req)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return rec
}

func (c *client) session() string { return c.cookies[cookieName] }

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) protocol.Error {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	if code == "" {
		return protocol.Error{}
	}
	e := decodeError(t, rec)
	if e.Code != code || e.Message == "" {
		t.Fatalf("error = %+v, want code %q", e, code)
	}
	return e
}

func (e *env) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.Read().QueryRow(query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// setup completes first-run setup through the API and returns the admin's client.
func (e *env) setup() *client {
	e.t.Helper()
	token, err := e.svc.InitSetup(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	c := e.client("")
	rec := c.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{
		SetupToken: token, Username: "admin", Password: goodPassword, DisplayName: "Admin", InstanceName: "Home",
	})
	wantStatus(e.t, rec, http.StatusCreated, "")
	return c
}

// addUser creates a member through an invite and returns its signed-in client.
func (e *env) addUser(username string, ip string) *client {
	e.t.Helper()
	_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{})
	if err != nil {
		e.t.Fatal(err)
	}
	c := e.client(ip)
	rec := c.do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Username: username, Password: goodPassword})
	wantStatus(e.t, rec, http.StatusCreated, "")
	return c
}

func TestSetupStatus(t *testing.T) {
	e := newEnv(t)
	c := e.client("")
	rec := c.do(http.MethodGet, "/api/v1/setup/status", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if !decodeBody[protocol.SetupStatus](t, rec).NeedsSetup {
		t.Error("needs_setup = false on an empty instance")
	}
	e.setup()
	rec = c.do(http.MethodGet, "/api/v1/setup/status", nil)
	if decodeBody[protocol.SetupStatus](t, rec).NeedsSetup {
		t.Error("needs_setup = true after setup")
	}
}

func TestSetupWithoutOrWithWrongTokenIs403(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.InitSetup(e.ctx); err != nil {
		t.Fatal(err)
	}
	c := e.client("")
	valid := protocol.SetupRequest{Username: "admin", Password: goodPassword, InstanceName: "Home"}
	for _, tok := range []string{"", strings.Repeat("a", 52)} {
		req := valid
		req.SetupToken = tok
		wantStatus(t, c.do(http.MethodPost, "/api/v1/setup", req), http.StatusForbidden, CodeForbidden)
	}
	// A body without the field at all behaves the same.
	wantStatus(t, c.do(http.MethodPost, "/api/v1/setup", `{"username":"admin","password":"`+goodPassword+`","instance_name":"Home"}`), http.StatusForbidden, CodeForbidden)
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 0 {
		t.Errorf("%d users created by rejected setups", n)
	}
	if len(c.cookies) != 0 {
		t.Error("a cookie was set for a rejected setup")
	}
}

func TestSetupWorksOnlyOnce(t *testing.T) {
	e := newEnv(t)
	token, err := e.svc.InitSetup(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(e.dataDir, auth.SetupTokenFile)
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("token file: %v", err)
	}
	c := e.client("")
	req := protocol.SetupRequest{SetupToken: token, Username: "Admin", Password: goodPassword, DisplayName: "The Admin", InstanceName: "Home Chat"}
	rec := c.do(http.MethodPost, "/api/v1/setup", req)
	wantStatus(t, rec, http.StatusCreated, "")
	me := decodeBody[protocol.AuthResponse](t, rec).User
	if me.Username != "admin" || me.Role != "admin" || me.DisplayName != "The Admin" || me.Kind != "human" {
		t.Errorf("user = %+v", me)
	}
	if e.st.InstanceName() != "Home Chat" {
		t.Errorf("instance_name = %q", e.st.InstanceName())
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Errorf("token file not deleted: %v", err)
	}
	// Logged in by the setup response itself.
	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")

	// Never again: right token, wrong token, any user -> 409.
	other := e.client("192.168.1.99")
	req.Username = "second"
	wantStatus(t, other.do(http.MethodPost, "/api/v1/setup", req), http.StatusConflict, CodeConflict)
	req.SetupToken = "wrong"
	wantStatus(t, other.do(http.MethodPost, "/api/v1/setup", req), http.StatusConflict, CodeConflict)
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("%d users, want 1", n)
	}
}

func TestSetupValidationErrorsListFields(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	c := e.client("")
	rec := c.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{SetupToken: token, Username: "A", Password: "short", InstanceName: ""})
	ed := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
	fields := ed.FieldErrors()
	for _, f := range []string{"username", "password", "instance_name"} {
		if fields[f] == "" {
			t.Errorf("no reason for %s in %v", f, fields)
		}
	}
	// The token survives validation failures.
	rec = c.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{SetupToken: token, Username: "admin", Password: goodPassword, InstanceName: "Home"})
	wantStatus(t, rec, http.StatusCreated, "")
}

func TestSessionCookieAttributesAndHashOnlyStorage(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	c := e.client("")
	rec := c.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{SetupToken: token, Username: "admin", Password: goodPassword, InstanceName: "Home"})
	wantStatus(t, rec, http.StatusCreated, "")

	raw := rec.Header().Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("Set-Cookie headers = %q", raw)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	ck := cookies[0]
	if ck.Name != "__Host-bubbles_session" || ck.Path != "/" || ck.Domain != "" || !ck.Secure || !ck.HttpOnly ||
		ck.SameSite != http.SameSiteStrictMode || ck.MaxAge != 90*24*3600 || len(ck.Value) != 52 {
		t.Errorf("cookie = %+v", ck)
	}
	wantRaw := "__Host-bubbles_session=" + ck.Value + "; Path=/; Max-Age=7776000; HttpOnly; Secure; SameSite=Strict"
	if raw[0] != wantRaw {
		t.Errorf("Set-Cookie = %q\nwant         %q", raw[0], wantRaw)
	}

	// The database holds the SHA-256, never the token.
	var stored []byte
	if err := e.db.Read().QueryRow(`SELECT id_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, auth.HashToken(ck.Value)) {
		t.Errorf("sessions.id_hash = %x, want SHA-256 of the cookie value", stored)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM sessions WHERE CAST(id_hash AS TEXT) LIKE ? OR user_agent LIKE ? OR ip LIKE ?`,
		`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ? OR target_id LIKE ? OR actor_ip LIKE ?`,
		`SELECT COUNT(*) FROM events WHERE payload LIKE ? OR type LIKE ? OR user_id LIKE ?`,
	} {
		pat := "%" + ck.Value + "%"
		if n := e.count(q, pat, pat, pat); n != 0 {
			t.Errorf("session token found in the database (%s)", q)
		}
	}
	// No password or setup token in the audit trail either.
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ? OR detail LIKE ?`, "%"+goodPassword+"%", "%"+token+"%"); n != 0 {
		t.Error("secret found in the audit log")
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("192.168.1.60")

	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)

	rec := c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "Admin", Password: goodPassword})
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.AuthResponse](t, rec).User; got.Username != "admin" || got.Role != "admin" {
		t.Errorf("login user = %+v", got)
	}
	me := c.do(http.MethodGet, "/api/v1/me", nil)
	wantStatus(t, me, http.StatusOK, "")
	got := decodeBody[protocol.Me](t, me)
	if got.Username != "admin" || got.Prefs == nil || got.AvatarFileID != nil || !strings.HasSuffix(got.CreatedAt, "Z") {
		t.Errorf("me = %+v", got)
	}

	first := c.session()
	// Logging in again rotates the session: the old cookie dies.
	old := e.client("192.168.1.60")
	old.cookies[cookieName] = first
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")
	if c.session() == first {
		t.Error("session token not rotated on login")
	}
	wantStatus(t, old.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)

	rec = c.do(http.MethodPost, "/api/v1/auth/logout", nil)
	wantStatus(t, rec, http.StatusNoContent, "")
	cleared := rec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != cookieName || cleared[0].MaxAge >= 0 || !cleared[0].Secure || !cleared[0].HttpOnly {
		t.Errorf("logout cookie = %v", cleared)
	}
	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
	// Logout without a session is still fine.
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/logout", nil), http.StatusNoContent, "")
}

func TestLoginFailureIsGeneric401(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	wrong := c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: "wrong password!"})
	unknown := c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "nobody", Password: "wrong password!"})
	wantStatus(t, wrong, http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, unknown, http.StatusUnauthorized, CodeUnauthorized)
	if wrong.Body.String() != unknown.Body.String() {
		t.Errorf("unknown user and wrong password differ:\n%s\n%s", wrong.Body.String(), unknown.Body.String())
	}
	if len(c.cookies) != 0 {
		t.Error("cookie set on failed login")
	}
	// Missing fields are a plain validation error.
	ed := wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", `{"username":"","password":""}`), http.StatusBadRequest, CodeBadRequest)
	if f := ed.FieldErrors(); f["username"] == "" || f["password"] == "" {
		t.Errorf("fields = %v", f)
	}
}

func TestLoginLockoutAfterFiveFailures(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	bad := protocol.LoginRequest{Username: "admin", Password: "wrong password!"}
	for i := 1; i <= 5; i++ {
		wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", bad), http.StatusUnauthorized, CodeUnauthorized)
	}
	rec := c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword})
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 15*60 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// Another IP is locked out of the same username too (per-username limit)...
	wantStatus(t, e.client("192.168.1.61").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusTooManyRequests, CodeRateLimited)
	// ... but not from other usernames.
	wantStatus(t, e.client("192.168.1.61").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "someone", Password: goodPassword}), http.StatusUnauthorized, CodeUnauthorized)

	e.clock.Advance(15*time.Minute + time.Second)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")
}

func TestLoginLockoutPerIP(t *testing.T) {
	e := newEnv(t)
	c := e.client("192.168.1.70")
	for i := 0; i < auth.LoginIPMaxFailures; i++ {
		wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "user" + strconv.Itoa(i), Password: "wrong password!"}), http.StatusUnauthorized, CodeUnauthorized)
	}
	rec := c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "fresh", Password: "wrong password!"})
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
}

func TestCommonAndWeakPasswordsRejected(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	if err := e.st.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		t.Fatal(err)
	}
	_, invite, _ := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{MaxUses: 10})
	user, _ := e.svc.UserByUsername(e.ctx, "admin")
	reset, _, _ := e.svc.CreateReset(e.ctx, audit.Console, user.ID)

	for _, pw := range []string{"1234567890", "QWERTYUIOP", "short"} {
		for name, rec := range map[string]*httptest.ResponseRecorder{
			"accept":   e.client("").do(http.MethodPost, "/api/v1/invites/"+invite+"/accept", protocol.AcceptInviteRequest{Username: "newbie", Password: pw}),
			"register": e.client("").do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Username: "newbie", Password: pw}),
			"reset":    e.client("").do(http.MethodPost, "/api/v1/password-resets/"+reset, protocol.ResetPasswordRequest{Password: pw}),
			"change":   admin.do(http.MethodPost, "/api/v1/me/password", protocol.ChangePasswordRequest{Current: goodPassword, New: pw}),
		} {
			ed := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
			f := ed.FieldErrors()
			if f["password"] == "" && f["new"] == "" {
				t.Errorf("%s with %q: no password reason in %v", name, pw, f)
			}
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE username = 'newbie'`); n != 0 {
		t.Error("account created with a rejected password")
	}
}

func TestInviteFlow(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{Role: auth.RoleGuest, MaxUses: 2, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	info := decodeBody[protocol.InviteInfo](t, c.do(http.MethodGet, "/api/v1/invites/"+code, nil))
	if !info.Valid || info.Role != "guest" || info.InstanceName != "Home" {
		t.Errorf("info = %+v", info)
	}
	if bad := decodeBody[protocol.InviteInfo](t, c.do(http.MethodGet, "/api/v1/invites/nope", nil)); bad.Valid || bad.Role != "" {
		t.Errorf("unknown code info = %+v", bad)
	}

	rec := c.do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Username: "Sam", Password: goodPassword, DisplayName: "Sam S"})
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.AuthResponse](t, rec).User; u.Role != "guest" || u.Username != "sam" {
		t.Errorf("user = %+v", u)
	}
	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")

	// Username taken -> 409 with the field; the use is not consumed.
	ed := wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Username: "sam", Password: goodPassword}), http.StatusConflict, CodeConflict)
	if ed.FieldErrors()["username"] == "" {
		t.Errorf("details = %v", ed.Details)
	}
	// Second (last) use.
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Username: "sue", Password: goodPassword}), http.StatusCreated, "")
	// Max uses reached.
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/invites/"+code+"/accept", protocol.AcceptInviteRequest{Username: "sid", Password: goodPassword}), http.StatusNotFound, CodeNotFound)
	if decodeBody[protocol.InviteInfo](t, c.do(http.MethodGet, "/api/v1/invites/"+code, nil)).Valid {
		t.Error("exhausted invite reported valid")
	}
}

func TestInviteExpiryAndRevoke(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	_, expiring, _ := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{ExpiresIn: time.Hour})
	inv, revoked, _ := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{})

	e.clock.Advance(61 * time.Minute)
	if decodeBody[protocol.InviteInfo](t, c.do(http.MethodGet, "/api/v1/invites/"+expiring, nil)).Valid {
		t.Error("expired invite reported valid")
	}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/invites/"+expiring+"/accept", protocol.AcceptInviteRequest{Username: "late", Password: goodPassword}), http.StatusNotFound, CodeNotFound)

	if err := e.svc.RevokeInvite(e.ctx, audit.Console, inv.ID); err != nil {
		t.Fatal(err)
	}
	if decodeBody[protocol.InviteInfo](t, c.do(http.MethodGet, "/api/v1/invites/"+revoked, nil)).Valid {
		t.Error("revoked invite reported valid")
	}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/invites/"+revoked+"/accept", protocol.AcceptInviteRequest{Username: "late", Password: goodPassword}), http.StatusNotFound, CodeNotFound)
	if n := e.count(`SELECT COUNT(*) FROM users WHERE username = 'late'`); n != 0 {
		t.Error("user created from a dead invite")
	}
}

func TestRegisterOnlyWhenOpen(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	req := protocol.RegisterRequest{Username: "walkin", Password: goodPassword, DisplayName: "Walk In"}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/register", req), http.StatusForbidden, CodeForbidden)
	if err := e.st.Set(e.ctx, audit.System, settings.KeyOpenRegistration, true); err != nil {
		t.Fatal(err)
	}
	rec := c.do(http.MethodPost, "/api/v1/register", req)
	wantStatus(t, rec, http.StatusCreated, "")
	if u := decodeBody[protocol.AuthResponse](t, rec).User; u.Role != "member" {
		t.Errorf("role = %q, want member", u.Role)
	}
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/register", req), http.StatusConflict, CodeConflict)
	if err := e.st.Set(e.ctx, audit.System, settings.KeyOpenRegistration, false); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/register", protocol.RegisterRequest{Username: "another", Password: goodPassword}), http.StatusForbidden, CodeForbidden)
}

func TestPasswordResetFlow(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	user, _ := e.svc.UserByUsername(e.ctx, "admin")
	code, _, err := e.svc.CreateReset(e.ctx, audit.Console, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := e.client("192.168.1.80")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/password-resets/"+code, protocol.ResetPasswordRequest{Password: otherPassword}), http.StatusNoContent, "")
	// The reset signs everyone out and the new password works.
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: otherPassword}), http.StatusOK, "")
	// One time only; unknown codes look the same.
	wantStatus(t, c.do(http.MethodPost, "/api/v1/password-resets/"+code, protocol.ResetPasswordRequest{Password: goodPassword}), http.StatusNotFound, CodeNotFound)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/password-resets/nope", protocol.ResetPasswordRequest{Password: goodPassword}), http.StatusNotFound, CodeNotFound)

	// Expiry after 24 hours.
	code2, _, _ := e.svc.CreateReset(e.ctx, audit.Console, user.ID)
	e.clock.Advance(auth.ResetTTL + time.Minute)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/password-resets/"+code2, protocol.ResetPasswordRequest{Password: goodPassword}), http.StatusNotFound, CodeNotFound)
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	laptop := e.setup()
	phone := e.client("192.168.1.81")
	wantStatus(t, phone.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")

	ed := wantStatus(t, laptop.do(http.MethodPost, "/api/v1/me/password", protocol.ChangePasswordRequest{Current: "not my password", New: otherPassword}), http.StatusBadRequest, CodeBadRequest)
	if ed.FieldErrors()["current"] == "" {
		t.Errorf("details = %v", ed.Details)
	}
	wantStatus(t, laptop.do(http.MethodPost, "/api/v1/me/password", protocol.ChangePasswordRequest{Current: goodPassword, New: otherPassword}), http.StatusNoContent, "")
	wantStatus(t, laptop.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
	wantStatus(t, phone.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: otherPassword}), http.StatusOK, "")
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusUnauthorized, CodeUnauthorized)
}

func TestMySessions(t *testing.T) {
	e := newEnv(t)
	laptop := e.setup()
	phone := e.client("192.168.1.82")
	wantStatus(t, phone.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")
	bob := e.addUser("bob", "192.168.1.83")

	rec := laptop.do(http.MethodGet, "/api/v1/me/sessions", nil)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeBody[protocol.SessionList](t, rec).Items
	if len(list) != 2 {
		t.Fatalf("%d sessions, want 2", len(list))
	}
	var current, other protocol.Session
	for _, s := range list {
		if s.Current {
			current = s
		} else {
			other = s
		}
	}
	if current.ID != auth.PublicSessionID(auth.HashToken(laptop.session())) || other.ID == "" || other.IP != "192.168.1.82" || other.UserAgent != "test-agent" || other.Kind != "web" {
		t.Errorf("current = %+v, other = %+v", current, other)
	}

	bobSessions := decodeBody[protocol.SessionList](t, bob.do(http.MethodGet, "/api/v1/me/sessions", nil)).Items
	if len(bobSessions) != 1 {
		t.Fatalf("bob sees %d sessions", len(bobSessions))
	}
	// Cannot touch another user's session.
	wantStatus(t, laptop.do(http.MethodDelete, "/api/v1/me/sessions/"+bobSessions[0].ID, nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")

	wantStatus(t, laptop.do(http.MethodDelete, "/api/v1/me/sessions/"+other.ID, nil), http.StatusNoContent, "")
	wantStatus(t, phone.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, laptop.do(http.MethodDelete, "/api/v1/me/sessions/"+other.ID, nil), http.StatusNotFound, CodeNotFound)

	// Revoking the current session signs this browser out.
	rec = laptop.do(http.MethodDelete, "/api/v1/me/sessions/"+current.ID, nil)
	wantStatus(t, rec, http.StatusNoContent, "")
	if cs := rec.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Errorf("cookie after revoking the current session = %v", cs)
	}
	wantStatus(t, laptop.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestCrossOriginPostsAreForbidden(t *testing.T) {
	e := newEnv(t)
	admin := e.setup()
	chg := protocol.ChangePasswordRequest{Current: goodPassword, New: otherPassword}

	setOrigin := func(v string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Origin", v) }
	}
	for name, mod := range map[string]func(*http.Request){
		"other host":        setOrigin("https://evil.example"),
		"other port":        setOrigin("https://example.com:9999"),
		"null origin":       setOrigin("null"),
		"unparseable":       setOrigin("::::"),
		"referer mismatch":  func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Referer", "https://evil.example/page") },
		"no origin/referer": func(r *http.Request) { r.Header.Del("Origin") },
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, admin.do(http.MethodPost, "/api/v1/me/password", chg, mod), http.StatusForbidden, CodeForbidden)
		})
	}
	// Nothing happened: the old password still works.
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")

	// Cross-origin logout and session revocation are refused as well.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/auth/logout", nil, setOrigin("https://evil.example")), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/me/sessions/abc", nil, setOrigin("https://evil.example")), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")

	// Login CSRF: a foreign Origin is refused even without a session.
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}, setOrigin("https://evil.example")), http.StatusForbidden, CodeForbidden)

	// Safe methods are not checked.
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/me", nil, setOrigin("https://evil.example")), http.StatusOK, "")

	// Same origin via Referer alone works; so do clients that send no
	// browser headers when they have no cookie session.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/auth/logout", nil, func(r *http.Request) {
		r.Header.Del("Origin")
		r.Header.Set("Referer", "https://"+r.Host+"/some/page")
	}), http.StatusNoContent, "")
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}, func(r *http.Request) { r.Header.Del("Origin") }), http.StatusOK, "")
}

func TestEndpointsNeedAuthentication(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.client("")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me"},
		{http.MethodPost, "/api/v1/me/password"},
		{http.MethodGet, "/api/v1/me/sessions"},
		{http.MethodDelete, "/api/v1/me/sessions/abc"},
	} {
		var body any
		if tc.method == http.MethodPost {
			body = protocol.ChangePasswordRequest{Current: "x", New: "y"}
		}
		wantStatus(t, c.do(tc.method, tc.path, body), http.StatusUnauthorized, CodeUnauthorized)
	}
	// A forged or stale cookie is no better.
	c.cookies[cookieName] = strings.Repeat("a", 52)
	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
}

func TestBodyHandling(t *testing.T) {
	e := newEnv(t)
	c := e.client("")
	login := "/api/v1/auth/login"

	big := `{"username":"admin","password":"` + strings.Repeat("x", MaxJSONBody) + `"}`
	wantStatus(t, c.do(http.MethodPost, login, big), http.StatusRequestEntityTooLarge, CodeTooLarge)

	asText := func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }
	wantStatus(t, c.do(http.MethodPost, login, `{"username":"a","password":"b"}`, asText), http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	noType := func(r *http.Request) { r.Header.Del("Content-Type") }
	wantStatus(t, c.do(http.MethodPost, login, `{"username":"a","password":"b"}`, noType), http.StatusUnsupportedMediaType, CodeUnsupportedMedia)

	for name, body := range map[string]string{
		"not json":      `username=a`,
		"truncated":     `{"username":"a"`,
		"wrong type":    `{"username":5,"password":"b"}`,
		"trailing data": `{"username":"a","password":"b"} {"x":1}`,
		"array":         `[1,2]`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, c.do(http.MethodPost, login, body), http.StatusBadRequest, CodeBadRequest)
		})
	}
	wantStatus(t, c.do(http.MethodPost, login, "", func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }), http.StatusBadRequest, CodeBadRequest)
	// charset parameters are fine.
	rec := c.do(http.MethodPost, login, `{"username":"a","password":"b"}`, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") })
	wantStatus(t, rec, http.StatusUnauthorized, CodeUnauthorized)
}

func TestSessionAPIRateLimit(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	// The clock is frozen: the bucket allows exactly the burst of 50.
	for i := 0; i < auth.APIBurst; i++ {
		if rec := c.do(http.MethodGet, "/api/v1/me", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
	}
	rec := c.do(http.MethodGet, "/api/v1/me", nil)
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
	// Other sessions are not affected, and tokens refill over time.
	other := e.client("192.168.1.91")
	wantStatus(t, other.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")
	wantStatus(t, other.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
	e.clock.Advance(time.Second)
	wantStatus(t, c.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
}

func TestCSPOnSPAResponses(t *testing.T) {
	e := newEnv(t)
	c := e.client("")
	for _, p := range []string{"/", "/channels/general", "/assets/app-1a2b.js", "/setup", "/invite/abc"} {
		rec := c.do(http.MethodGet, p, nil)
		if got := rec.Header().Get("Content-Security-Policy"); got != ContentSecurityPolicy {
			t.Errorf("GET %s: CSP = %q", p, got)
		}
	}
	// API answers carry it as well.
	if rec := c.do(http.MethodGet, "/api/v1/setup/status", nil); rec.Header().Get("Content-Security-Policy") != ContentSecurityPolicy {
		t.Error("no CSP on API responses")
	}
}

func TestNetworkGateCoversAuthRoutes(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	req := protocol.SetupRequest{SetupToken: token, Username: "admin", Password: goodPassword, InstanceName: "Home"}
	outsider := e.client("8.8.8.8")
	wantStatus(t, outsider.do(http.MethodGet, "/api/v1/setup/status", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, outsider.do(http.MethodPost, "/api/v1/setup", req), http.StatusForbidden, CodeForbidden)
	if n := e.count(`SELECT COUNT(*) FROM users`); n != 0 {
		t.Fatal("setup reached from a public address")
	}
	// The allowed_cidrs setting opens the gate without a restart.
	if err := e.st.Set(e.ctx, audit.System, settings.KeyAllowedCIDRs, []string{"8.8.8.0/24"}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, outsider.do(http.MethodGet, "/api/v1/setup/status", nil), http.StatusOK, "")
	if err := e.st.Set(e.ctx, audit.System, settings.KeyAllowedCIDRs, []string{}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, outsider.do(http.MethodGet, "/api/v1/setup/status", nil), http.StatusForbidden, CodeForbidden)
}

func TestAuditTrailOfAPIActions(t *testing.T) {
	e := newEnv(t)
	c := e.setup()
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: "wrong password!"}), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: goodPassword}), http.StatusOK, "")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/me/password", protocol.ChangePasswordRequest{Current: goodPassword, New: otherPassword}), http.StatusNoContent, "")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/auth/logout", nil), http.StatusNoContent, "")
	for action, want := range map[string]int{
		audit.ActionSetup: 1, audit.ActionLoginFailure: 1, audit.ActionLoginSuccess: 1,
		audit.ActionPasswordChange: 1, audit.ActionLogout: 1, audit.ActionSettingsUpdated: 1,
	} {
		if got := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action); got != want {
			t.Errorf("audit %s = %d, want %d", action, got, want)
		}
	}
	// Audit rows carry the client IP, never the password.
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE actor_ip = '192.168.1.50' AND actor_kind = 'user'`); n == 0 {
		t.Error("no audit row with the client IP")
	}
	for _, secret := range []string{goodPassword, otherPassword, "wrong password!"} {
		if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ?`, "%"+secret+"%"); n != 0 {
			t.Errorf("secret %q in the audit log", secret)
		}
	}
}

// TestSpecOperationsAreRouted proves every operation in docs/openapi.yaml has
// a handler (the contract harness covers the opposite direction: handlers
// without a spec entry fail the exchange check).
func TestSpecOperationsAreRouted(t *testing.T) {
	e := newEnv(t)
	doc, _ := loadSpec(t)
	c := e.client("")
	n := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			base := apiPrefix
			if op.Servers != nil && len(*op.Servers) > 0 {
				base = strings.TrimSuffix((*op.Servers)[0].URL, "/")
			}
			concrete := strings.NewReplacer("{code}", "x", "{id}", "x", "{user_id}", "x", "{message_id}", "x", "{emoji}", "x", "{token}", "x").Replace(path)
			rec := c.do(method, base+concrete, nil)
			if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "no such API endpoint") {
				t.Errorf("%s %s%s (%s) is documented but not routed", method, base, concrete, op.OperationID)
			}
			n++
		}
	}
	if n < 13 {
		t.Errorf("checked %d operations", n)
	}
}
