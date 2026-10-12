package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWindowAllowed(t *testing.T) {
	tests := []struct {
		name       string
		windowName string
		want       bool
	}{
		{"sign-in window", "login", true},
		{"main window with the handoff page", "main", false},
		{"empty window name", "", false},
		{"case differs", "Login", false},
		{"similar name", "login2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowAllowed(tt.windowName); got != tt.want {
				t.Fatalf("windowAllowed(%q) = %v, want %v", tt.windowName, got, tt.want)
			}
		})
	}
}

func TestOriginAllowed(t *testing.T) {
	const handoff = "https://bubbles.lan:28225"
	tests := []struct {
		name      string
		goos      string
		origin    string
		fetchSite string
		want      bool
	}{
		{"local origin on windows", "windows", "http://wails.localhost", "same-origin", true},
		{"local origin on macos", "darwin", "wails://wails", "same-origin", true},
		{"local origin on linux", "linux", "wails://wails", "", true},
		{"windows origin on macos", "darwin", "http://wails.localhost", "", false},
		{"macos origin on windows", "windows", "wails://wails", "", false},
		{"remote https origin", "windows", "https://bubbles.example.com", "cross-site", false},
		{"handoff URL origin", "windows", handoff, "cross-site", false},
		{"handoff URL origin with same-origin fetch site", "windows", handoff, "same-origin", false},
		{"null origin from a sandboxed page", "windows", "null", "cross-site", false},
		{"empty origin, cross-site", "windows", "", "cross-site", false},
		{"empty origin, no fetch metadata", "windows", "", "", false},
		{"empty origin, same-origin GET", "windows", "", "same-origin", true},
		{"empty origin, same-origin on macos", "darwin", "", "same-origin", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := originAllowed(tt.goos, tt.origin, tt.fetchSite); got != tt.want {
				t.Fatalf("originAllowed(%q, %q, %q) = %v, want %v", tt.goos, tt.origin, tt.fetchSite, got, tt.want)
			}
		})
	}
}

func TestOriginGateMiddleware(t *testing.T) {
	served := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusOK)
	})
	h := originGate("windows")(next)
	tests := []struct {
		name       string
		path       string
		origin     string
		fetchSite  string
		wantStatus int
	}{
		{"local runtime call", "/wails/runtime", "http://wails.localhost", "same-origin", http.StatusOK},
		{"remote runtime call", "/wails/runtime", "https://bubbles.example.com", "cross-site", http.StatusForbidden},
		{"remote runtime preflight", "/wails/runtime", "https://bubbles.example.com", "", http.StatusForbidden},
		{"remote script load of runtime", "/wails/runtime.js", "", "cross-site", http.StatusForbidden},
		{"remote stream poll", "/wails/stream", "https://bubbles.example.com", "cross-site", http.StatusForbidden},
		{"local app asset", "/index.html", "", "none", http.StatusOK},
		{"remote app asset is not runtime", "/assets/app.js", "https://bubbles.example.com", "cross-site", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://wails.localhost"+tt.path, nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			rec := httptest.NewRecorder()
			served = 0
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusForbidden && served != 0 {
				t.Fatalf("a refused request reached the next handler")
			}
		})
	}
}

// TestAuthServiceGuardFirst checks that every AuthService method runs the
// window gate before anything else. The service has no trayApp, so a method
// that reached its body would panic. A refused call must return the gate's
// error and leave the zero view state.
func TestAuthServiceGuardFirst(t *testing.T) {
	denied := errors.New("denied")
	calls := 0
	ctxSeen := false
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	svc := &AuthService{guard: func(c context.Context) error {
		calls++
		ctxSeen = c.Value(ctxKey{}) == "sentinel"
		return denied
	}}
	tests := []struct {
		name string
		call func() error
	}{
		{"State", func() error { _, err := svc.State(ctx); return err }},
		{"Discover", func() error { _, err := svc.Discover(ctx); return err }},
		{"Prepare", func() error { _, err := svc.Prepare(ctx, "https://x", ""); return err }},
		{"Confirm", func() error { _, err := svc.Confirm(ctx, "AA:BB", true); return err }},
		{"Login", func() error { _, err := svc.Login(ctx, "u", "p", "d"); return err }},
		{"Back", func() error { _, err := svc.Back(ctx); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls = 0
			ctxSeen = false
			if err := tt.call(); !errors.Is(err, denied) {
				t.Fatalf("err = %v, want the gate error", err)
			}
			if calls != 1 {
				t.Fatalf("guard called %d times, want 1", calls)
			}
			if !ctxSeen {
				t.Fatalf("guard did not receive the binding context")
			}
		})
	}
}
