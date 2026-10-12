package app

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

var cheapParams = auth.Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}

const (
	ownerPassword = "correct horse battery"
	instanceName  = "Test House"
)

func startCheap(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	return startWith(t, cfg, func(o *Options) { o.AuthParams = cheapParams })
}

// currentDB reads the App's database under the lifecycle lock (a factory
// reset replaces it).
func currentDB(a *App) *store.DB {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	return a.db
}

func dbCount(t *testing.T, a *App, query string, args ...any) int {
	t.Helper()
	var n int
	if err := currentDB(a).Read().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// web is a browser-like client: same-origin requests with a session cookie.
type web struct {
	t      *testing.T
	h      *harness
	base   string
	cookie *http.Cookie
}

func newWeb(t *testing.T, h *harness) *web {
	return &web{t: t, h: h, base: "https://" + h.app.HTTPSAddr().String()}
}

func (w *web) do(method, path string, body any, mods ...func(*http.Request)) (*http.Response, []byte) {
	w.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			w.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, w.base+path, rdr)
	if err != nil {
		w.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", w.base)
	if w.cookie != nil {
		req.AddCookie(w.cookie)
	}
	for _, m := range mods {
		m(req)
	}
	client := w.h.httpsClient()
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	return res, readAll(w.t, res)
}

func (w *web) want(status int, method, path string, body any, mods ...func(*http.Request)) []byte {
	w.t.Helper()
	res, b := w.do(method, path, body, mods...)
	if res.StatusCode != status {
		w.t.Fatalf("%s %s = %d %s, want %d", method, path, res.StatusCode, b, status)
	}
	return b
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Del("Origin")
	}
}

func setupToken(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, auth.SetupTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// setupOwner completes first-run setup and returns the owner's client and
// the setup token it used.
func setupOwner(t *testing.T, h *harness) (*web, string) {
	t.Helper()
	w := newWeb(t, h)
	token := setupToken(t, h.app.cfg.DataDir)
	res, body := w.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{
		SetupToken: token, Username: "owner", Password: ownerPassword, DisplayName: "Owner", InstanceName: instanceName,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", res.StatusCode, body)
	}
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			w.cookie = c
		}
	}
	if w.cookie == nil {
		t.Fatal("no session cookie")
	}
	return w, token
}

// content is what the reset tests create before resetting.
type content struct {
	channelID string
	appToken  string
	instance  string
}

// fill posts a canary message with a photo, a reply and a reaction, pins
// it, and creates an app with a token.
func fill(t *testing.T, w *web) content {
	t.Helper()
	a := w.h.app
	if err := a.settings.Set(context.Background(), audit.System, settings.KeyDiskReserveBytes, 0); err != nil {
		t.Fatal(err)
	}
	var c content
	if err := currentDB(a).Read().QueryRow(`SELECT id FROM channels WHERE name = 'general'`).Scan(&c.channelID); err != nil {
		t.Fatal(err)
	}
	if err := currentDB(a).Read().QueryRow(`SELECT id FROM instance`).Scan(&c.instance); err != nil {
		t.Fatal(err)
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = part.Write(filestest.JPEGWithMeta(64, 48, 1))
	_ = mw.Close()
	var file protocol.File
	body := w.want(http.StatusCreated, http.MethodPost, "/api/v1/files", form.String(), func(r *http.Request) {
		r.Header.Set("Content-Type", mw.FormDataContentType())
	})
	if err := json.Unmarshal(body, &file); err != nil {
		t.Fatal(err)
	}
	var msg protocol.Message
	body = w.want(http.StatusCreated, http.MethodPost, "/api/v1/channels/"+c.channelID+"/messages",
		protocol.CreateMessageRequest{Body: "canary-" + c.instance, FileIDs: []string{file.ID}})
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatal(err)
	}
	root := msg.ID
	w.want(http.StatusCreated, http.MethodPost, "/api/v1/channels/"+c.channelID+"/messages",
		protocol.CreateMessageRequest{Body: "a reply", ThreadRootID: &root})
	w.want(http.StatusOK, http.MethodPut, "/api/v1/messages/"+msg.ID+"/reactions/%F0%9F%91%8D", nil)
	w.want(http.StatusOK, http.MethodPut, "/api/v1/channels/"+c.channelID+"/pins/"+msg.ID, nil)

	var app protocol.App
	if err := json.Unmarshal(w.want(http.StatusCreated, http.MethodPost, "/api/v1/admin/apps", protocol.CreateAppRequest{Name: "netdiag"}), &app); err != nil {
		t.Fatal(err)
	}
	var issued protocol.IssuedAppToken
	if err := json.Unmarshal(w.want(http.StatusCreated, http.MethodPost, "/api/v1/admin/apps/"+app.ID+"/tokens",
		protocol.CreateAppTokenRequest{Scopes: []string{protocol.ScopeMessagesRead}}), &issued); err != nil {
		t.Fatal(err)
	}
	c.appToken = issued.Token
	w.want(http.StatusOK, http.MethodGet, "/api/v1/apps/self", nil, bearer(c.appToken))
	return c
}

// watchWS opens the owner's WebSocket and reports the close code it ends
// with.
func watchWS(t *testing.T, w *web) <-chan websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hdr := http.Header{}
	hdr.Set("Origin", w.base)
	hdr.Set("Cookie", w.cookie.Name+"="+w.cookie.Value)
	conn, _, err := websocket.Dial(ctx, "wss://"+w.h.app.HTTPSAddr().String()+"/api/v1/ws",
		&websocket.DialOptions{HTTPClient: w.h.httpsClient(), HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	if _, _, err := conn.Read(ctx); err != nil { // hello
		t.Fatalf("read hello: %v", err)
	}
	codes := make(chan websocket.StatusCode, 1)
	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				codes <- websocket.CloseStatus(err)
				return
			}
		}
	}()
	return codes
}

func wantClose(t *testing.T, codes <-chan websocket.StatusCode, want int) {
	t.Helper()
	select {
	case got := <-codes:
		if int(got) != want {
			t.Errorf("WebSocket closed with %d, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Errorf("WebSocket not closed")
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// waitTrashGone waits for the background delete of the reset's files.
func waitTrashGone(t *testing.T, dir string) {
	t.Helper()
	waitFor(t, "the trash to be deleted", func() bool {
		for _, name := range dirEntries(t, dir) {
			if strings.HasPrefix(name, reset.TrashPrefix) {
				return false
			}
		}
		return true
	})
}

func TestResetClearContentOverHTTP(t *testing.T) {
	cfg := testConfig(t)
	h := startCheap(t, cfg)
	w, _ := setupOwner(t, h)
	c := fill(t, w)
	keep := map[string]int{}
	for _, table := range []string{"users", "sessions", "channels", "memberships", "apps", "app_tokens", "settings"} {
		keep[table] = dbCount(t, h.app, `SELECT count(*) FROM `+table)
	}
	var preview protocol.ResetPreview
	if err := json.Unmarshal(w.want(http.StatusOK, http.MethodGet, "/api/v1/admin/reset/preview", nil), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Messages < 2 || preview.Files != 1 || preview.Bytes == 0 || preview.Users != 1 {
		t.Errorf("preview = %+v", preview)
	}
	codes := watchWS(t, w)

	body := w.want(http.StatusOK, http.MethodPost, "/api/v1/admin/reset",
		protocol.ResetRequest{Mode: protocol.ResetContent, Confirm: instanceName, Password: ownerPassword})
	var res protocol.ContentCleared
	if err := json.Unmarshal(body, &res); err != nil || res.Epoch != 2 {
		t.Fatalf("answer %s: %v", body, err)
	}
	wantClose(t, codes, events.CloseContentCleared)

	for _, table := range []string{"messages", "files", "alerts", "events", "reactions", "pins", "mentions", "attachments", "messages_fts_docsize"} {
		if n := dbCount(t, h.app, `SELECT count(*) FROM `+table); n != 0 {
			t.Errorf("%s: %d rows after clear", table, n)
		}
	}
	for table, n := range keep {
		if got := dbCount(t, h.app, `SELECT count(*) FROM `+table); got != n {
			t.Errorf("%s: %d rows, had %d", table, got, n)
		}
	}
	if n := dbCount(t, h.app, `SELECT count(*) FROM instance WHERE id = ? AND epoch = 2`, c.instance); n != 1 {
		t.Error("instance id changed or epoch not bumped")
	}
	if n := dbCount(t, h.app, `SELECT count(*) FROM audit_log WHERE action = ?`, reset.ActionContentCleared); n != 1 {
		t.Errorf("%d content_cleared audit rows", n)
	}
	waitTrashGone(t, cfg.DataDir)
	if names := dirEntries(t, filepath.Join(cfg.DataDir, config.DirFiles)); len(names) != 0 {
		t.Errorf("files/ holds %v", names)
	}
	// The owner stays signed in, the app token works, channels are empty.
	w.want(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	w.want(http.StatusOK, http.MethodGet, "/api/v1/apps/self", nil, bearer(c.appToken))
	if b := w.want(http.StatusOK, http.MethodGet, "/api/v1/channels/"+c.channelID+"/messages", nil); strings.Contains(string(b), "canary-") {
		t.Errorf("messages after clear: %s", b)
	}
	if b := w.want(http.StatusOK, http.MethodGet, "/api/v1/bootstrap", nil); !strings.Contains(string(b), `"epoch":2`) {
		t.Errorf("bootstrap does not carry epoch 2: %s", b)
	}
}

func TestResetFactoryOverHTTP(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rotate, keep bool
	}{
		{"keep CA", false, false},
		{"rotate CA keep settings", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			h := startCheap(t, cfg)
			w, oldSetupToken := setupOwner(t, h)
			c := fill(t, w)
			oldCA := h.app.Certs().CA()
			oldLeaf := servedLeaf(t, h)
			oldVAPID, err := os.ReadFile(filepath.Join(cfg.DataDir, push.VAPIDFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{
				filepath.Join(cfg.DataDir, config.DirLogs, "bubbles.log"),
				filepath.Join(cfg.DataDir, config.DirBackups, store.SnapshotPrefix+"1-2.db"),
				filepath.Join(cfg.DataDir, config.DirBackups, "weekly.bbk"),
			} {
				if err := os.WriteFile(p, []byte("old "+c.instance), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			codes := watchWS(t, w)

			body := w.want(http.StatusOK, http.MethodPost, "/api/v1/admin/reset", protocol.ResetRequest{
				Mode: protocol.ResetFactory, Confirm: instanceName, Password: ownerPassword,
				RotateCA: tc.rotate, KeepSettings: tc.keep,
			})
			var res protocol.FactoryReset
			if err := json.Unmarshal(body, &res); err != nil {
				t.Fatal(err)
			}
			wantClose(t, codes, events.CloseServerReset)
			newCA := h.app.Certs().CA()
			if len(res.SetupToken) != 52 || res.SetupToken != setupToken(t, cfg.DataDir) || res.CARotated != tc.rotate ||
				res.CAFingerprint != newCA.Fingerprint() || !strings.HasPrefix(res.SetupURL, "https://127.0.0.1:") {
				t.Errorf("answer = %+v", res)
			}
			if out := h.stderr.String(); !strings.Contains(out, "factory reset") || !strings.Contains(out, res.SetupToken) {
				t.Errorf("console does not show the reset:\n%s", out)
			}

			// Only the fresh instance row, the first audit row and the kept
			// settings remain.
			if n := dbCount(t, h.app, `SELECT count(*) FROM instance WHERE id != ?`, c.instance); n != 1 {
				t.Error("no new instance id")
			}
			var detail string
			if err := currentDB(h.app).Read().QueryRow(`SELECT detail FROM audit_log WHERE action = ? AND actor_id IS NULL`, reset.ActionFactoryReset).Scan(&detail); err != nil ||
				detail != fmt.Sprintf(`{"keep_settings":%v,"rotate_ca":%v}`, tc.keep, tc.rotate) {
				t.Errorf("audit detail = %q, %v", detail, err)
			}
			for table, want := range map[string]int{"audit_log": 1, "users": 0, "sessions": 0, "channels": 0, "apps": 0, "app_tokens": 0, "messages": 0, "files": 0, "events": 0, "push_subscriptions": 0} {
				if n := dbCount(t, h.app, `SELECT count(*) FROM `+table); n != want {
					t.Errorf("%s: %d rows, want %d", table, n, want)
				}
			}
			// fill set disk_reserve_bytes; setup set instance_name.
			if n := dbCount(t, h.app, `SELECT count(*) FROM settings`); (tc.keep && n != 2) || (!tc.keep && n != 0) {
				t.Errorf("settings rows = %d", n)
			}
			if tc.keep && dbCount(t, h.app, `SELECT count(*) FROM settings WHERE key = 'instance_name' AND value = ?`, `"`+instanceName+`"`) != 1 {
				t.Error("instance name not kept")
			}
			if v, _ := os.ReadFile(filepath.Join(cfg.DataDir, push.VAPIDFile)); len(v) == 0 || bytes.Equal(v, oldVAPID) {
				t.Error("VAPID keys not replaced")
			}
			for _, sub := range []string{config.DirLogs, config.DirBackups} {
				if names := dirEntries(t, filepath.Join(cfg.DataDir, sub)); len(names) != 0 {
					t.Errorf("%s/ = %v", sub, names)
				}
			}
			waitTrashGone(t, cfg.DataDir)
			if names := dirEntries(t, filepath.Join(cfg.DataDir, config.DirFiles)); len(names) != 0 {
				t.Errorf("files/ = %v", names)
			}

			// Old credentials are dead; the setup wizard is back.
			w.want(http.StatusUnauthorized, http.MethodGet, "/api/v1/me", nil)
			w.want(http.StatusUnauthorized, http.MethodGet, "/api/v1/apps/self", nil, bearer(c.appToken))
			anon := newWeb(t, h)
			if b := anon.want(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil); !strings.Contains(string(b), `"needs_setup":true`) {
				t.Errorf("setup status = %s", b)
			}

			// CA kept, or rotated with the old one useless.
			if !tc.rotate {
				if newCA.Fingerprint() != oldCA.Fingerprint() {
					t.Error("CA changed without rotate_ca")
				}
			} else {
				if newCA.Fingerprint() == oldCA.Fingerprint() {
					t.Fatal("CA not rotated")
				}
				pool := x509.NewCertPool()
				pool.AddCert(newCA.Cert)
				if _, err := oldLeaf.Verify(x509.VerifyOptions{Roots: pool}); err == nil {
					t.Error("the old leaf verifies against the new CA")
				}
				if _, err := servedLeaf(t, h).Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
					t.Errorf("served leaf does not verify against the new CA: %v", err)
				}
				if h.app.mdnsRefreshes.Load() == 0 {
					t.Error("mDNS not refreshed after the CA changed")
				}
			}

			setup := protocol.SetupRequest{Username: "owner2", Password: ownerPassword, DisplayName: "Owner", InstanceName: "Next"}
			setup.SetupToken = oldSetupToken
			anon.want(http.StatusForbidden, http.MethodPost, "/api/v1/setup", setup)
			setup.SetupToken = res.SetupToken
			anon.want(http.StatusCreated, http.MethodPost, "/api/v1/setup", setup)
		})
	}
}

func TestResetDeniedAttemptsAreAuditedAndWebResetCanBeDisabled(t *testing.T) {
	h := startCheap(t, testConfig(t))
	w, _ := setupOwner(t, h)
	c := fill(t, w)
	w.want(http.StatusForbidden, http.MethodPost, "/api/v1/admin/reset",
		protocol.ResetRequest{Mode: protocol.ResetFactory, Confirm: "test house", Password: ownerPassword})
	w.want(http.StatusForbidden, http.MethodPost, "/api/v1/admin/reset",
		protocol.ResetRequest{Mode: protocol.ResetContent, Confirm: instanceName, Password: "not my password"})
	rows, err := currentDB(h.app).Read().Query(`SELECT detail FROM audit_log WHERE action = ? ORDER BY id`, reset.ActionResetDenied)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		details = append(details, d)
	}
	rows.Close()
	if strings.Join(details, " ") != `{"mode":"factory","reason":"confirm"} {"mode":"content","reason":"password"}` {
		t.Errorf("denied audit rows = %v", details)
	}

	if err := h.app.settings.Set(context.Background(), audit.System, settings.KeyWebResetEnabled, false); err != nil {
		t.Fatal(err)
	}
	w.want(http.StatusForbidden, http.MethodPost, "/api/v1/admin/reset",
		protocol.ResetRequest{Mode: protocol.ResetContent, Confirm: instanceName, Password: ownerPassword})
	if n := dbCount(t, h.app, `SELECT count(*) FROM messages`); n == 0 {
		t.Error("a refused reset deleted messages")
	}
	if n := dbCount(t, h.app, `SELECT count(*) FROM instance WHERE id = ? AND epoch = 1`, c.instance); n != 1 {
		t.Error("a refused reset touched the instance")
	}
}

func TestMaintenanceModeRefusesEverythingButHealthz(t *testing.T) {
	h := startCheap(t, testConfig(t))
	w, _ := setupOwner(t, h)
	h.app.gate.enter()

	res, body := w.do(http.MethodGet, "/api/v1/bootstrap", nil)
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") != "5" ||
		string(bytes.TrimSpace(body)) != `{"error":{"code":"unavailable","message":"resetting"}}` {
		t.Errorf("API in maintenance: %d %q %s", res.StatusCode, res.Header.Get("Retry-After"), body)
	}
	w.want(http.StatusOK, http.MethodGet, "/healthz", nil)
	hdr := http.Header{}
	hdr.Set("Origin", w.base)
	hdr.Set("Cookie", w.cookie.Name+"="+w.cookie.Value)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, wsRes, err := websocket.Dial(ctx, "wss://"+h.app.HTTPSAddr().String()+"/api/v1/ws",
		&websocket.DialOptions{HTTPClient: h.httpsClient(), HTTPHeader: hdr})
	if err == nil {
		conn.CloseNow()
		t.Error("WebSocket upgrade accepted in maintenance")
	} else if wsRes == nil || wsRes.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("WebSocket upgrade in maintenance: %v", err)
	}
	onboard, err := plainClient().Get("http://" + h.app.HTTPAddr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, onboard); onboard.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("onboarding in maintenance: %d", onboard.StatusCode)
	}

	h.app.gate.leave()
	w.want(http.StatusOK, http.MethodGet, "/api/v1/bootstrap", nil)
}

func TestMaintenanceDrainWaitsForRequestsInFlight(t *testing.T) {
	var gate maintenance
	release := make(chan struct{})
	entered := make(chan struct{})
	srv := httptest.NewServer(gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate.self(r.Context()) != 1 {
			t.Error("request context not marked by the gate")
		}
		close(entered)
		<-release
	})))
	defer srv.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if res, err := http.Get(srv.URL + "/slow"); err == nil {
			res.Body.Close()
		}
	}()
	<-entered
	gate.enter()
	if gate.drain(0, 50*time.Millisecond) {
		t.Error("drain returned while a request was in flight")
	}
	if !gate.drain(1, 50*time.Millisecond) {
		t.Error("drain did not exclude the caller's own request")
	}
	res, err := http.Get(srv.URL + "/new")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("new request in maintenance: %d", res.StatusCode)
	}
	close(release)
	<-done
	if !gate.drain(0, 5*time.Second) {
		t.Error("drain did not see the request finish")
	}
	if gate.self(context.Background()) != 0 || !gate.closed() {
		t.Error("gate state")
	}
}

// TestResetRecoveredAtStartup: a reset journaled but cut short is completed
// by the next start before any listener opens.
func TestResetRecoveredAtStartup(t *testing.T) {
	for _, mode := range []reset.Mode{reset.ModeContent, reset.ModeFactory} {
		t.Run(string(mode), func(t *testing.T) {
			cfg := testConfig(t)
			h := startCheap(t, cfg)
			w, _ := setupOwner(t, h)
			c := fill(t, w)
			h.stop(t)
			m := &reset.Marker{ID: "01JRECOVERTEST", Mode: mode, Done: []string{}}
			if mode == reset.ModeContent {
				m.Epoch = 2
			}
			if err := reset.WriteMarker(cfg.DataDir, m); err != nil {
				t.Fatal(err)
			}

			h2 := startCheap(t, cfg)
			if pending, err := reset.ReadMarker(cfg.DataDir); err != nil || pending != nil {
				t.Fatalf("marker after start: %+v, %v", pending, err)
			}
			if n := dbCount(t, h2.app, `SELECT count(*) FROM messages`); n != 0 {
				t.Errorf("%d messages after the recovered reset", n)
			}
			anon := newWeb(t, h2)
			status := string(anon.want(http.StatusOK, http.MethodGet, "/api/v1/setup/status", nil))
			if mode == reset.ModeContent {
				if n := dbCount(t, h2.app, `SELECT count(*) FROM instance WHERE id = ? AND epoch = 2`, c.instance); n != 1 {
					t.Error("epoch not bumped by the recovered clear")
				}
				if !strings.Contains(status, `"needs_setup":false`) {
					t.Errorf("setup status = %s", status)
				}
				return
			}
			if n := dbCount(t, h2.app, `SELECT count(*) FROM instance WHERE id != ?`, c.instance); n != 1 {
				t.Error("no new instance after the recovered factory reset")
			}
			if !strings.Contains(status, `"needs_setup":true`) {
				t.Errorf("setup status = %s", status)
			}
			token := setupToken(t, cfg.DataDir)
			waitFor(t, "the banner to show the setup token", func() bool { return strings.Contains(h2.stderr.String(), token) })
		})
	}
}

// TestResetWith2000FilesReturnsQuickly: files are moved aside and deleted in
// the background, so the API call does not wait for them.
func TestResetWith2000FilesReturnsQuickly(t *testing.T) {
	cfg := testConfig(t)
	h := startCheap(t, cfg)
	w, _ := setupOwner(t, h)
	var owner string
	if err := currentDB(h.app).Read().QueryRow(`SELECT id FROM users`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	err := currentDB(h.app).Do(context.Background(), func(tx *store.Tx) error {
		for i := range 2000 {
			sha := fmt.Sprintf("%064x", i+1)
			if _, err := tx.Exec(`INSERT INTO files (id, sha256, size, mime, category, original_name, uploader_id, created_at)
				VALUES (?, ?, 1024, 'image/png', 'image', 'p.png', ?, ?)`, tx.NewID(), sha, owner, tx.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	blob := bytes.Repeat([]byte("x"), 1024)
	for i := range 2000 {
		sha := fmt.Sprintf("%064x", i+1)
		p := filepath.Join(cfg.DataDir, config.DirFiles, sha[0:2], sha[2:4], sha)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	w.want(http.StatusOK, http.MethodPost, "/api/v1/admin/reset",
		protocol.ResetRequest{Mode: protocol.ResetFactory, Confirm: instanceName, Password: ownerPassword})
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("factory reset with 2,000 files took %v", d)
	}
	waitTrashGone(t, cfg.DataDir)
	var left int
	_ = filepath.WalkDir(filepath.Join(cfg.DataDir, config.DirFiles), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			left++
		}
		return err
	})
	if left != 0 {
		t.Errorf("%d blobs left", left)
	}
}

func TestResetRefusesConcurrentAndClosed(t *testing.T) {
	h := startCheap(t, testConfig(t))
	h.app.resetMu.Lock()
	if _, err := h.app.Reset(context.Background(), reset.Request{Mode: reset.ModeContent}); !errors.Is(err, reset.ErrInProgress) {
		t.Errorf("concurrent reset = %v", err)
	}
	h.app.resetMu.Unlock()
	h.stop(t)
	if _, err := h.app.Reset(context.Background(), reset.Request{Mode: reset.ModeContent}); !errors.Is(err, store.ErrClosed) {
		t.Errorf("reset after shutdown = %v", err)
	}
}

func TestAdminBackupDownloadsEncryptedArchive(t *testing.T) {
	h := startCheap(t, testConfig(t))
	w, _ := setupOwner(t, h)
	res, body := w.do(http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{Passphrase: "a long backup passphrase"})
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/octet-stream" ||
		!strings.HasPrefix(res.Header.Get("Content-Disposition"), `attachment; filename="bubbles-`) {
		t.Fatalf("backup: %d %v", res.StatusCode, res.Header)
	}
	if !bytes.HasPrefix(body, []byte("age-encryption.org/v1")) {
		t.Errorf("not an age archive: %q", body[:min(32, len(body))])
	}
}
