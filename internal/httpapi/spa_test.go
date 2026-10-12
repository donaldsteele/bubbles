package httpapi

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
)

func spaTestHandler() http.Handler {
	files := http.FileServerFS(fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>app</title>")},
		"sw.js":                {Data: []byte("self.skipWaiting()")},
		"manifest.webmanifest": {Data: []byte(`{"name":"bubbles"}`)},
		"assets/app-1a2b.js":   {Data: []byte("console.log(1)")},
	})
	return New(Deps{Version: "v", SPA: files, AllowedCIDRs: func() []netip.Prefix { return nil }})
}

func TestSPAServiceWorkerAndManifest(t *testing.T) {
	h := spaTestHandler()
	tests := []struct {
		path, ctype, body string
	}{
		{"/sw.js", "text/javascript; charset=utf-8", "skipWaiting"},
		{"/manifest.webmanifest", "application/manifest+json", `"bubbles"`},
	}
	for _, tt := range tests {
		rec := do(h, http.MethodGet, tt.path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", tt.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tt.ctype {
			t.Errorf("GET %s Content-Type = %q, want %q", tt.path, got, tt.ctype)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", tt.path, got)
		}
		if !strings.Contains(rec.Body.String(), tt.body) {
			t.Errorf("GET %s body = %q", tt.path, rec.Body.String())
		}
	}
}

func TestSPAClientRoutesServeIndex(t *testing.T) {
	h := spaTestHandler()
	for _, p := range []string{"/", "/c/abc", "/c/abc/t/def", "/settings/profile", "/admin"} {
		rec := do(h, http.MethodGet, p, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>app</title>") {
			t.Errorf("GET %s = %d %q", p, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q", p, got)
		}
	}
	if rec := do(h, http.MethodGet, "/missing.js", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing file = %d, want 404", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/assets/app-1a2b.js", ""); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("hashed asset is not immutable")
	}
}

func TestSPAAPINotFoundIsJSON(t *testing.T) {
	h := spaTestHandler()
	rec := do(h, http.MethodGet, "/api/v1/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	if strings.Contains(rec.Body.String(), "<title>") {
		t.Error("API 404 returned the SPA")
	}
}
