package onboarding

import (
	"encoding/pem"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/donaldsteele/bubbles/internal/tlsca"
)

var (
	caOnce sync.Once
	testCA *tlsca.CA
)

func sharedCA(t *testing.T) *tlsca.CA {
	t.Helper()
	caOnce.Do(func() {
		ca, err := tlsca.NewCA(tlsca.CAOptions{InstanceName: "Home"})
		if err != nil {
			panic(err)
		}
		testCA = ca
	})
	return testCA
}

func testHandler(t *testing.T, withCA bool) http.Handler {
	t.Helper()
	ca := sharedCA(t)
	return New(Options{
		InstanceName: func() string { return "Home Chat" },
		CA: func() *tlsca.CA {
			if withCA {
				return ca
			}
			return nil
		},
		HTTPSPort: 8443,
		URLs:      func() []string { return []string{"https://192.168.1.20:8443/", "https://10.0.0.5:8443/"} },
		Hosts:     func() []string { return []string{"bubbles.local", "127.0.0.1", "::1"} },
	})
}

type reqOpt func(*http.Request)

func withUA(ua string) reqOpt       { return func(r *http.Request) { r.Header.Set("User-Agent", ua) } }
func withRemote(addr string) reqOpt { return func(r *http.Request) { r.RemoteAddr = addr } }
func withHost(host string) reqOpt   { return func(r *http.Request) { r.Host = host } }

func get(h http.Handler, method, target string, opts ...reqOpt) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://192.168.1.20:8080"+target, nil)
	req.RemoteAddr = "192.168.1.77:50000"
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPage(t *testing.T) {
	ca := sharedCA(t)
	rec := get(testHandler(t, true), http.MethodGet, "/", withUA("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/604.1"))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("status = %d, type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Connect to Home Chat",
		ca.Fingerprint(),
		ca.Cert.Subject.CommonName,
		`href="https://192.168.1.20:8443/"`,
		`href="https://10.0.0.5:8443/"`,
		`src="/qr.svg?u=https%3a%2f%2f192.168.1.20%3a8443%2f"`,
		`href="/onboarding.css"`,
		"<details open>\n<summary>iPhone and iPad</summary>",
		"<summary>Android</summary>", "<summary>Windows</summary>", "<summary>macOS</summary>",
		"<summary>Linux</summary>", "<summary>Firefox on Linux</summary>",
		"Import-Certificate -FilePath", "security add-trusted-cert -r trustRoot", "update-ca-certificates", "trust anchor",
		"client isolation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if n := strings.Count(body, "<details open>"); n != 1 {
		t.Errorf("%d sections open, want 1", n)
	}
	for _, bad := range []string{"<script", "style=", "<style"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q (CSP forbids inline code and styles)", bad)
		}
	}
	if rec.Header().Get("Content-Security-Policy") != ContentSecurityPolicy || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestPageOpensFirefoxOnLinux(t *testing.T) {
	rec := get(testHandler(t, true), http.MethodGet, "/", withUA("Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"))
	body := rec.Body.String()
	if !strings.Contains(body, "<details open>\n<summary>Linux</summary>") || !strings.Contains(body, "<details open>\n<summary>Firefox on Linux</summary>") {
		t.Error("Linux and Firefox sections not both open")
	}
}

func TestStylesheet(t *testing.T) {
	rec := get(testHandler(t, true), http.MethodGet, "/onboarding.css")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" || !strings.Contains(rec.Body.String(), "--accent") {
		t.Errorf("status = %d, type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestCADownloads(t *testing.T) {
	ca := sharedCA(t)
	h := testHandler(t, true)

	rec := get(h, http.MethodGet, "/ca.crt")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-x509-ca-cert" || rec.Body.String() != string(ca.DER()) {
		t.Errorf("/ca.crt: status = %d, type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="bubbles-ca.crt"` {
		t.Errorf("/ca.crt disposition = %q", cd)
	}

	rec = get(h, http.MethodGet, "/ca.pem")
	block, _ := pem.Decode(rec.Body.Bytes())
	if rec.Code != http.StatusOK || block == nil || string(block.Bytes) != string(ca.DER()) {
		t.Errorf("/ca.pem: status = %d", rec.Code)
	}

	rec = get(h, http.MethodGet, "/ca.mobileconfig")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/x-apple-aspen-config" || rec.Body.String() != string(ca.MobileConfig()) {
		t.Errorf("/ca.mobileconfig: status = %d, type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestBYOModeHasNoCA(t *testing.T) {
	h := testHandler(t, false)
	rec := get(h, http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "supplied by its administrator") || strings.Contains(rec.Body.String(), "/ca.crt") {
		t.Errorf("BYO page: status = %d", rec.Code)
	}
	for _, p := range []string{"/ca.crt", "/ca.pem", "/ca.mobileconfig"} {
		if rec := get(h, http.MethodGet, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, rec.Code)
		}
	}
}

func TestQRCode(t *testing.T) {
	h := testHandler(t, true)
	for _, u := range []string{
		"https://192.168.1.20:8443/",
		"https://10.0.0.5:8443",
		"https://bubbles.local:8443/",
		"https://BUBBLES.local:8443/",
		"https://[::1]:8443/",
		"https://192.168.1.20:8443/invite/abc_DEF-123",
	} {
		rec := get(h, http.MethodGet, "/qr.svg?u="+urlQuery(u))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
			t.Errorf("u=%s: status = %d", u, rec.Code)
			continue
		}
		if err := wellFormedXML(rec.Body.String()); err != nil || !strings.HasPrefix(rec.Body.String(), "<svg") {
			t.Errorf("u=%s: not an SVG document: %v", u, err)
		}
	}
	for _, u := range []string{
		"",
		"http://192.168.1.20:8443/",
		"https://evil.example:8443/",
		"https://8.8.8.8:8443/",
		"https://192.168.1.20:9999/",
		"https://192.168.1.20/",
		"https://192.168.1.20:8443/login",
		"https://192.168.1.20:8443/?next=x",
		"https://192.168.1.20:8443/#frag",
		"https://user@192.168.1.20:8443/",
		"https://192.168.1.20:8443/invite/bad%20code",
		"https://192.168.1.20:8443/invite/" + strings.Repeat("a", 600),
		"javascript:alert(1)",
	} {
		if rec := get(h, http.MethodGet, "/qr.svg?u="+urlQuery(u)); rec.Code != http.StatusBadRequest {
			t.Errorf("u=%q: status = %d, want 400", u, rec.Code)
		}
	}
}

func TestInvitePage(t *testing.T) {
	h := testHandler(t, true)
	rec := get(h, http.MethodGet, "/invite/abc_DEF-123")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "You are invited to Home Chat") ||
		!strings.Contains(body, `href="https://192.168.1.20:8443/invite/abc_DEF-123"`) ||
		!strings.Contains(body, `src="/qr.svg?u=https%3a%2f%2f192.168.1.20%3a8443%2finvite%2fabc_DEF-123"`) {
		t.Errorf("invite page: status = %d", rec.Code)
	}
	if rec := get(h, http.MethodGet, "/invite/bad.code"); rec.Code != http.StatusNotFound {
		t.Errorf("bad invite code: status = %d", rec.Code)
	}
}

func TestRedirectsEverythingElseToHTTPS(t *testing.T) {
	h := testHandler(t, true)
	tests := []struct {
		method, path string
		opts         []reqOpt
		want         string
	}{
		{http.MethodGet, "/api/v1/me", nil, "https://192.168.1.20:8443/api/v1/me"},
		{http.MethodPost, "/api/v1/auth/login", nil, "https://192.168.1.20:8443/api/v1/auth/login"},
		{http.MethodGet, "/channels/x?y=1", nil, "https://192.168.1.20:8443/channels/x?y=1"},
		{http.MethodPost, "/ca.crt", nil, "https://192.168.1.20:8443/ca.crt"},
		{http.MethodGet, "/healthz", []reqOpt{withHost("bubbles.local:8080")}, "https://bubbles.local:8443/healthz"},
		{http.MethodGet, "/", []reqOpt{withHost("192.168.1.20")}, ""},
		{http.MethodGet, "/x", []reqOpt{withHost("[::1]:8080")}, "https://[::1]:8443/x"},
	}
	for _, tt := range tests {
		rec := get(h, tt.method, tt.path, tt.opts...)
		if tt.want == "" {
			if rec.Code != http.StatusOK {
				t.Errorf("%s %s: status = %d, want the page", tt.method, tt.path, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != tt.want {
			t.Errorf("%s %s: status = %d, Location = %q, want 308 %s", tt.method, tt.path, rec.Code, rec.Header().Get("Location"), tt.want)
		}
	}
}

func TestRedirectOnlyTrustsOwnHost(t *testing.T) {
	h := testHandler(t, true)
	noURLs := New(Options{HTTPSPort: 8443, Hosts: func() []string { return []string{"bubbles.local"} }})
	tests := []struct {
		name, target, host string
		handler            http.Handler
		want               string // "" means 400: no advertised URL to fall back to
	}{
		{"absolute-form target to foreign host", "http://evil.com/x", "", h, "https://192.168.1.20:8443/x"},
		{"forged Host header", "http://192.168.1.20:8080/x", "evil.com:8080", h, "https://192.168.1.20:8443/x"},
		{"malformed Host header", "http://192.168.1.20:8080/x", "evil host", h, "https://192.168.1.20:8443/x"},
		{"own SAN name is kept", "http://192.168.1.20:8080/x", "bubbles.local:8080", h, "https://bubbles.local:8443/x"},
		{"own address is kept", "http://192.168.1.20:8080/x?y=1", "[::1]:8080", h, "https://[::1]:8443/x?y=1"},
		{"foreign host with nothing advertised", "http://192.168.1.20:8080/x", "evil.com", noURLs, ""},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, tt.target, nil)
		req.RemoteAddr = "192.168.1.77:50000"
		if tt.host != "" {
			req.Host = tt.host
		}
		rec := httptest.NewRecorder()
		tt.handler.ServeHTTP(rec, req)
		if tt.want == "" {
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", tt.name, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != tt.want {
			t.Errorf("%s: status = %d, Location = %q, want 308 %s", tt.name, rec.Code, rec.Header().Get("Location"), tt.want)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", tt.name, got)
		}
	}
}

func TestNoCookiesAnywhere(t *testing.T) {
	h := testHandler(t, true)
	for _, p := range []string{"/", "/onboarding.css", "/ca.crt", "/ca.pem", "/ca.mobileconfig",
		"/qr.svg?u=https%3A%2F%2F192.168.1.20%3A8443%2F", "/qr.svg", "/invite/abc", "/api/v1/me", "/login"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			rec := get(h, m, p)
			if c := rec.Header().Values("Set-Cookie"); len(c) != 0 {
				t.Errorf("%s %s set cookies %v", m, p, c)
			}
		}
	}
}

func TestGuard(t *testing.T) {
	h := testHandler(t, true)
	for _, p := range []string{"/", "/ca.crt", "/api/v1/me"} {
		if rec := get(h, http.MethodGet, p, withRemote("8.8.8.8:1234")); rec.Code != http.StatusForbidden {
			t.Errorf("%s from a public address: status = %d", p, rec.Code)
		}
	}
	allowed := New(Options{HTTPSPort: 8443, AllowedCIDRs: func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")} }})
	if rec := get(allowed, http.MethodGet, "/", withRemote("8.8.8.8:1234")); rec.Code != http.StatusOK {
		t.Errorf("allowed CIDR: status = %d", rec.Code)
	}
}

func TestDetectOS(t *testing.T) {
	tests := []struct {
		ua   string
		want OS
	}{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15", OSiOS},
		{"Mozilla/5.0 (iPad; CPU OS 16_6 like Mac OS X)", OSiOS},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/126.0 Mobile Safari/537.36", OSAndroid},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/126.0 Safari/537.36 Edg/126.0", OSWindows},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 Version/17.5 Safari/605.1.15", OSMac},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0", OSLinux},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36", OSUnknown},
		{"curl/8.0", OSUnknown},
		{"", OSUnknown},
	}
	for _, tt := range tests {
		if got := DetectOS(tt.ua); got != tt.want {
			t.Errorf("DetectOS(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
	if !IsFirefox("Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0") || IsFirefox("Chrome/126.0") {
		t.Error("IsFirefox misclassifies")
	}
}

func urlQuery(s string) string {
	r := strings.NewReplacer("%", "%25", ":", "%3A", "/", "%2F", "?", "%3F", "#", "%23", "&", "%26", "@", "%40", "[", "%5B", "]", "%5D", " ", "%20", "=", "%3D")
	return r.Replace(s)
}

func wellFormedXML(s string) error {
	dec := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
