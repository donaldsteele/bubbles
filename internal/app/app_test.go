package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// syncBuffer is a goroutine-safe bytes.Buffer for the banner.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeTickers hands out manually driven tickers, keyed by interval.
type fakeTickers struct {
	mu sync.Mutex
	m  map[time.Duration]chan time.Time
}

type fakeTicker struct{ c chan time.Time }

func (f fakeTicker) C() <-chan time.Time { return f.c }
func (f fakeTicker) Stop()               {}

func (f *fakeTickers) New(d time.Duration) Ticker {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[time.Duration]chan time.Time{}
	}
	c := make(chan time.Time)
	f.m[d] = c
	return fakeTicker{c}
}

// tick fires the ticker with interval d and blocks until a job loop takes it.
func (f *fakeTickers) tick(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		c := f.m[d]
		f.mu.Unlock()
		if c != nil {
			select {
			case c <- time.Now():
				return
			case <-time.After(time.Until(deadline)):
				t.Fatalf("no job consumed the %v tick", d)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ticker with interval %v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// lanAddrs is a settable LAN address source.
type lanAddrs struct {
	mu    sync.Mutex
	addrs []netip.Addr
}

func (l *lanAddrs) set(a ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addrs = nil
	for _, s := range a {
		l.addrs = append(l.addrs, netip.MustParseAddr(s))
	}
}

func (l *lanAddrs) get() ([]netip.Addr, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]netip.Addr(nil), l.addrs...), nil
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type harness struct {
	app     *App
	stderr  *syncBuffer
	tickers *fakeTickers
	lan     *lanAddrs
	clock   *clock
	cancel  context.CancelFunc
	done    chan error
	stopped sync.Once
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	return config.Config{DataDir: dir, HTTPSAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", MDNSName: "bubbles", NoMDNS: true}
}

func start(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	return startWith(t, cfg, nil)
}

// startWith is start with a hook to adjust the Options.
func startWith(t *testing.T, cfg config.Config, adjust func(*Options)) *harness {
	t.Helper()
	return startLogged(t, cfg, nil, adjust)
}

// startLogged is startWith with a logger for the App (nil discards).
func startLogged(t *testing.T, cfg config.Config, logger *slog.Logger, adjust func(*Options)) *harness {
	t.Helper()
	h := &harness{stderr: &syncBuffer{}, tickers: &fakeTickers{}, lan: &lanAddrs{}, clock: &clock{now: time.Now()}}
	h.lan.set("192.168.1.20")
	opts := Options{
		Stderr:    h.stderr,
		Now:       h.clock.Now,
		NewTicker: h.tickers.New,
		LANAddrs:  h.lan.get,
		Hostname:  func() (string, error) { return "test-host", nil },
	}
	if adjust != nil {
		adjust(&opts)
	}
	a, err := New(context.Background(), cfg, logger, opts)
	if err != nil {
		t.Fatal(err)
	}
	h.app = a
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- a.Run(ctx) }()
	t.Cleanup(func() { h.stop(t) })
	return h
}

func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.stopped.Do(func() {
		h.cancel()
		select {
		case err := <-h.done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})
}

func (h *harness) httpsClient() *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(h.app.Certs().CA().Cert)
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
		Timeout:   10 * time.Second,
	}
}

func plainClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestServesHTTPSAndOnboarding(t *testing.T) {
	h := start(t, testConfig(t))
	httpsBase := "https://" + h.app.HTTPSAddr().String()
	httpBase := "http://" + h.app.HTTPAddr().String()

	res, err := h.httpsClient().Get(httpsBase + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	var health struct{ Status, Version string }
	if err := json.Unmarshal(readAll(t, res), &health); err != nil || res.StatusCode != 200 || health.Status != "ok" {
		t.Fatalf("healthz: %d %+v %v", res.StatusCode, health, err)
	}
	if res.TLS == nil || res.TLS.Version < tls.VersionTLS12 {
		t.Errorf("TLS state = %+v", res.TLS)
	}

	res, err = h.httpsClient().Get(httpsBase + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	if body := readAll(t, res); res.StatusCode != 404 || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Errorf("unknown API: %d %s", res.StatusCode, body)
	}

	res, err = plainClient().Get(httpBase + "/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(readAll(t, res))
	if block == nil || !bytes.Equal(block.Bytes, h.app.Certs().CA().DER()) {
		t.Error("/ca.pem is not the CA")
	}

	res, err = plainClient().Get(httpBase + "/api/v1/me")
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, res)
	want := "https://127.0.0.1:" + strconv.Itoa(h.app.HTTPSPort()) + "/api/v1/me"
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != want {
		t.Errorf("redirect = %d %q, want 308 %q", res.StatusCode, res.Header.Get("Location"), want)
	}
	if len(res.Header.Values("Set-Cookie")) != 0 {
		t.Error("onboarding listener set a cookie")
	}

	banner := h.stderr.String()
	for _, want := range []string{
		"https://127.0.0.1:" + strconv.Itoa(h.app.HTTPSPort()) + "/",
		"http://" + h.app.HTTPAddr().String() + "/",
		h.app.Certs().CA().Fingerprint(),
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner lacks %q:\n%s", want, banner)
		}
	}
}

func TestGuardOnBothListeners(t *testing.T) {
	h := start(t, testConfig(t))
	for name, srv := range map[string]*http.Server{"https": h.app.httpsSrv, "http": h.app.httpSrv} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "8.8.8.8:4000"
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s listener: public source got %d, want 403", name, rec.Code)
		}
	}
}

func TestURLsUseLANAddressesWhenBoundToAll(t *testing.T) {
	h := start(t, testConfig(t))
	all := fakeListener{addr: &net.TCPAddr{IP: net.IPv6unspecified, Port: 28225}}
	h.lan.set("192.168.1.20", "fe80::1", "10.0.0.5")
	if got := strings.Join(baseURLs("https", h.app.urlHosts(all), 28225), " "); got != "https://192.168.1.20:28225/ https://10.0.0.5:28225/" {
		t.Errorf("URLs = %s", got)
	}
	h.lan.set()
	if got := h.app.urlHosts(all); len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("hosts without LAN = %v", got)
	}
	bound := fakeListener{addr: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 28225}}
	if got := h.app.urlHosts(bound); len(got) != 1 || got[0] != "10.1.2.3" {
		t.Errorf("hosts of a bound listener = %v", got)
	}
}

// fakeListener only reports an address.
type fakeListener struct {
	net.Listener
	addr net.Addr
}

func (f fakeListener) Addr() net.Addr { return f.addr }

func servedLeaf(t *testing.T, h *harness) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", h.app.HTTPSAddr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

func hasIP(c *x509.Certificate, ip string) bool {
	for _, a := range c.IPAddresses {
		if a.String() == ip {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSANWatcherHotSwapsLeaf(t *testing.T) {
	h := start(t, testConfig(t))
	if leaf := servedLeaf(t, h); !hasIP(leaf, "192.168.1.20") {
		t.Fatalf("initial leaf SANs = %v", leaf.IPAddresses)
	}
	h.lan.set("192.168.1.77")
	h.tickers.tick(t, SANWatchInterval)
	waitFor(t, "re-issued leaf", func() bool { return hasIP(h.app.Certs().Current().Leaf, "192.168.1.77") })
	leaf := servedLeaf(t, h)
	if !hasIP(leaf, "192.168.1.77") || hasIP(leaf, "192.168.1.20") {
		t.Errorf("served leaf after DHCP change has %v", leaf.IPAddresses)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "192.168.1.77", Roots: poolOf(h.app.Certs().CA())}); err != nil {
		t.Errorf("hot-swapped leaf does not verify: %v", err)
	}
}

func poolOf(ca *tlsca.CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func TestRenewalJob(t *testing.T) {
	h := start(t, testConfig(t))
	before := h.app.Certs().Current().Leaf
	h.clock.Add(tlsca.LeafValidity - tlsca.RenewBefore + time.Hour)
	h.tickers.tick(t, RenewCheckInterval)
	waitFor(t, "renewed leaf", func() bool {
		return h.app.Certs().Current().Leaf.SerialNumber.Cmp(before.SerialNumber) != 0
	})
	if got := h.app.Certs().Current().Leaf.NotAfter; !got.After(before.NotAfter) {
		t.Errorf("renewed NotAfter %v not after %v", got, before.NotAfter)
	}
}

func TestShutdownIsIdempotentAndRestartReusesCA(t *testing.T) {
	cfg := testConfig(t)
	h := start(t, cfg)
	fp := h.app.Certs().CA().Fingerprint()
	h.stop(t)
	if err := h.app.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
	h2 := start(t, cfg)
	if h2.app.Certs().CA().Fingerprint() != fp {
		t.Error("restart created a new CA")
	}
}

func TestNewFailsCleanlyWhenPortTaken(t *testing.T) {
	cfg := testConfig(t)
	h := start(t, cfg)
	cfg2 := testConfig(t)
	cfg2.HTTPAddr = h.app.HTTPAddr().String()
	_, err := New(context.Background(), cfg2, nil, Options{LANAddrs: func() ([]netip.Addr, error) { return nil, nil }})
	if err == nil || !strings.Contains(err.Error(), "--http-addr") {
		t.Fatalf("New with a taken port: %v", err)
	}
}

// TestFirstRunSetupEndToEnd covers the wiring of setup token, auth routes,
// settings (instance name) and onboarding over real TLS.
func TestFirstRunSetupEndToEnd(t *testing.T) {
	cfg := testConfig(t)
	h := start(t, cfg)
	httpsBase := "https://" + h.app.HTTPSAddr().String()
	httpBase := "http://" + h.app.HTTPAddr().String()

	// The banner prints the token; the same token sits in setup-token.txt.
	var banner string
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(banner, "setup token") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		banner = h.stderr.String()
	}
	file, err := os.ReadFile(filepath.Join(cfg.DataDir, "setup-token.txt"))
	if err != nil {
		t.Fatalf("setup-token.txt: %v", err)
	}
	token := strings.TrimSpace(string(file))
	if len(token) != 52 || !strings.Contains(banner, token) {
		t.Fatalf("token %q not in banner:\n%s", token, banner)
	}

	post := func(path, body string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodPost, httpsBase+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", httpsBase)
		res, err := h.httpsClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res, readAll(t, res)
	}
	setup := func(tok string) (*http.Response, []byte) {
		return post("/api/v1/setup", `{"setup_token":"`+tok+`","username":"owner","password":"correct horse battery","display_name":"Owner","instance_name":"Test House"}`)
	}

	if res, body := setup("wrong"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong token: %d %s", res.StatusCode, body)
	}
	res, body := setup(token)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", res.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "__Host-bubbles_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Errorf("session cookie = %+v", cookie)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "setup-token.txt")); !os.IsNotExist(err) {
		t.Errorf("setup-token.txt remains after setup: %v", err)
	}
	if res, body := setup(token); res.StatusCode != http.StatusConflict {
		t.Errorf("second setup: %d %s", res.StatusCode, body)
	}

	// The chat API is wired: the setup session bootstraps into #general.
	req, _ := http.NewRequest(http.MethodGet, httpsBase+"/api/v1/bootstrap", nil)
	req.AddCookie(cookie)
	res3, err := h.httpsClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	boot := readAll(t, res3)
	if res3.StatusCode != http.StatusOK || !strings.Contains(string(boot), `"instance_id"`) || !strings.Contains(string(boot), `"name":"general"`) {
		t.Errorf("bootstrap: %d %s", res3.StatusCode, boot)
	}

	// The instance name reaches the onboarding page.
	res2, err := plainClient().Get(httpBase + "/")
	if err != nil {
		t.Fatal(err)
	}
	if page := readAll(t, res2); !strings.Contains(string(page), "Test House") {
		t.Error("onboarding page does not show the instance name")
	}
}

// TestPushIsWired: first run creates <data>/vapid.json (0600 where the OS has
// modes) and the push endpoints answer from it once an admin enables web push.
func TestPushIsWired(t *testing.T) {
	cfg := testConfig(t)
	h := start(t, cfg)
	base := "https://" + h.app.HTTPSAddr().String()

	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, "vapid.json"))
	if err != nil {
		t.Fatalf("vapid.json: %v", err)
	}
	var keys struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(raw, &keys); err != nil || keys.PublicKey == "" {
		t.Fatalf("vapid.json = %q (%v)", raw, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(cfg.DataDir, "vapid.json"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("vapid.json mode = %v, want 0600", fi.Mode().Perm())
		}
	}

	token := strings.TrimSpace(readFile(t, filepath.Join(cfg.DataDir, "setup-token.txt")))
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/setup",
		strings.NewReader(`{"setup_token":"`+token+`","username":"owner","password":"correct horse battery","display_name":"Owner","instance_name":"Test House"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", base)
	res, err := h.httpsClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, res)
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "__Host-bubbles_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatalf("no session cookie after setup (status %d)", res.StatusCode)
	}
	get := func() (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/push/vapid-public-key", nil)
		req.AddCookie(cookie)
		res, err := h.httpsClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(readAll(t, res))
	}
	if code, body := get(); code != http.StatusNotFound {
		t.Fatalf("key while web push is off: %d %s, want 404", code, body)
	}
	if err := h.app.settings.Set(context.Background(), audit.System, settings.KeyWebPushEnabled, true); err != nil {
		t.Fatal(err)
	}
	if code, body := get(); code != http.StatusOK || !strings.Contains(body, keys.PublicKey) {
		t.Fatalf("key with web push on: %d %s, want 200 with %s", code, body, keys.PublicKey)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
