package httpapi

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/donaldsteele/bubbles/internal/tlsca"
)

var testSPA = http.FileServerFS(fstest.MapFS{
	"index.html":         {Data: []byte("<!doctype html><title>app</title>")},
	"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
})

// testHandler is the HTTPS handler without auth routes. Every exchange is
// validated against docs/openapi.yaml (see contractHandler).
func testHandler(t testing.TB, allowed ...netip.Prefix) http.Handler {
	return contractHandler(t, New(Deps{Version: "v9.9.9", SPA: testSPA, AllowedCIDRs: func() []netip.Prefix { return allowed }}))
}

func do(h http.Handler, method, target, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	if remote == "" {
		remote = "192.168.1.50:1234"
	}
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorDetail {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", rec.Body.String(), err)
	}
	return body.Error
}

func TestHealthz(t *testing.T) {
	rec := do(testHandler(t), http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Status != "ok" || got.Version != "v9.9.9" {
		t.Errorf("body = %q (%v)", rec.Body.String(), err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get(RequestIDHeader) == "" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestUnknownAPIIsJSON404(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/unknown"},
		{http.MethodGet, "/api/v1/nope/deeper"},
		{http.MethodPost, "/api/v1/me"},
		{http.MethodDelete, "/api/"},
	} {
		rec := do(testHandler(t), tc.method, tc.path, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d", tc.method, tc.path, rec.Code)
			continue
		}
		if e := decodeError(t, rec); e.Code != CodeNotFound || e.Message == "" {
			t.Errorf("%s %s: error = %+v", tc.method, tc.path, e)
		}
	}
}

func TestSPAServingAndFallback(t *testing.T) {
	tests := []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/", "<title>app</title>", "no-cache", 200},
		{"/channels/general", "<title>app</title>", "no-cache", 200},
		{"/admin/users/01ABC", "<title>app</title>", "no-cache", 200},
		{"/assets/app-1a2b.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/assets/missing.js", "", "", 404},
		{"/favicon.ico", "", "", 404},
	}
	for _, tt := range tests {
		rec := do(testHandler(t), http.MethodGet, tt.path, "")
		if rec.Code != tt.wantStatus {
			t.Errorf("GET %s: status = %d, want %d", tt.path, rec.Code, tt.wantStatus)
			continue
		}
		if !strings.Contains(rec.Body.String(), tt.wantBody) {
			t.Errorf("GET %s: body = %q", tt.path, rec.Body.String())
		}
		if tt.wantCache != "" && rec.Header().Get("Cache-Control") != tt.wantCache {
			t.Errorf("GET %s: Cache-Control = %q", tt.path, rec.Header().Get("Cache-Control"))
		}
	}
	if rec := do(testHandler(t), http.MethodPost, "/channels", ""); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST to SPA route: status = %d, Allow = %q", rec.Code, rec.Header().Get("Allow"))
	}
}

func TestSecurityHeaders(t *testing.T) {
	want := map[string]string{
		"Content-Security-Policy":    "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         PermissionsPolicy,
	}
	for _, p := range []string{"/", "/channels/x", "/assets/app-1a2b.js", "/healthz", "/api/unknown"} {
		rec := do(testHandler(t), http.MethodGet, p, "")
		for k, v := range want {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("GET %s: %s = %q, want %q", p, k, got, v)
			}
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("GET %s: HSTS must not be set", p)
		}
	}
}

func TestGuard(t *testing.T) {
	rec := do(testHandler(t), http.MethodGet, "/healthz", "8.8.8.8:5555")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("public source: status = %d", rec.Code)
	}
	if e := decodeError(t, rec); e.Code != CodeForbidden {
		t.Errorf("error = %+v", e)
	}
	if rec := do(testHandler(t), http.MethodGet, "/", "8.8.8.8:5555"); rec.Code != http.StatusForbidden {
		t.Errorf("public source on SPA: status = %d", rec.Code)
	}
	if rec := do(testHandler(t, netip.MustParsePrefix("8.8.8.0/24")), http.MethodGet, "/healthz", "8.8.8.8:5555"); rec.Code != http.StatusOK {
		t.Errorf("allowed CIDR: status = %d", rec.Code)
	}
	if rec := do(testHandler(t), http.MethodGet, "/healthz", "192.168.1.9:5555"); rec.Code != http.StatusOK {
		t.Errorf("LAN source: status = %d", rec.Code)
	}
}

func TestRecoverer(t *testing.T) {
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		requestID, recoverer(slogDiscard()))
	rec := do(h, http.MethodGet, "/x", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if e := decodeError(t, rec); e.Code != CodeInternal {
		t.Errorf("error = %+v", e)
	}
}

func TestTLSServer(t *testing.T) {
	ca, err := tlsca.NewCA(tlsca.CAOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(tlsca.Discover(tlsca.SANInput{}), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(testHandler(t), func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return leaf, nil }, nil)
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.TLS = srv.TLSConfig
	ts.Config.ErrorLog = srv.ErrorLog
	ts.StartTLS()
	defer ts.Close()

	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	res, err := client.Get(strings.Replace(ts.URL, "127.0.0.1", "localhost", 1) + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d", res.StatusCode)
	}

	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MaxVersion: tls.VersionTLS11}}}
	if _, err := old.Get(ts.URL + "/healthz"); err == nil {
		t.Error("TLS 1.1 handshake succeeded")
	}
}

func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }
