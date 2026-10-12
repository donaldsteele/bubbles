package bubblesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestPinnedTLS(t *testing.T) {
	pki := newPKI(t)
	other := newPKI(t)
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer bub_test" {
			t.Error("missing bearer")
		}
		_ = json.NewEncoder(w).Encode(protocol.PostedAlert{Message: protocol.Message{ID: "m1"}})
	})
	tests := []struct {
		name   string
		fp     string
		sendCA bool
		want   error
	}{
		{"pinned ok", pki.caFP, true, nil},
		{"wrong pin", other.caFP, true, ErrServerIdentityChanged},
		{"leaf pin is not the CA", pki.leafFP, true, ErrServerIdentityChanged},
		{"CA not presented", pki.caFP, false, ErrServerIdentityChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			srv := pki.server(t, h, tc.sendCA)
			c := newClient(t, srv, tc.fp)
			m, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"})
			if tc.want == nil {
				if err != nil || m.ID != "m1" {
					t.Fatalf("err=%v m=%v", err, m)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if hits.Load() != 0 {
				t.Fatal("a request reached the server before the pin check passed")
			}
		})
	}
}

func TestNewValidation(t *testing.T) {
	for _, cfg := range []Config{
		{BaseURL: ""},
		{BaseURL: "http://127.0.0.1:28225"},
		{BaseURL: "https://127.0.0.1:28225/x"},
		{BaseURL: "https://127.0.0.1:28225", CAFingerprint: "nope"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}

func TestAPIErrors(t *testing.T) {
	pki := newPKI(t)
	tests := []struct {
		name   string
		status int
		code   string
		header map[string]string
		is     error
	}{
		{"unauthorized", 401, "unauthorized", nil, ErrUnauthorized},
		{"rate limited", 429, "rate_limited", map[string]string{"Retry-After": "7"}, ErrRateLimited},
		{"unavailable", 503, "unavailable", nil, ErrUnavailable},
		{"internal", 500, "internal", nil, ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(protocol.ErrorBody{Error: protocol.Error{Code: tc.code, Message: "msg",
					Details: map[string]any{"fields": map[string]any{"title": "required"}}}})
			}), true)
			c := newClient(t, srv, pki.caFP)
			err := c.ResolveAlert(context.Background(), "k", "")
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Code != tc.code || ae.Message != "msg" {
				t.Fatalf("api error = %+v", ae)
			}
			if ae.FieldErrors()["title"] != "required" {
				t.Fatalf("fields = %v", ae.FieldErrors())
			}
			if tc.status == 429 && ae.RetryAfter != 7*time.Second {
				t.Fatalf("RetryAfter = %v", ae.RetryAfter)
			}
			if errors.Is(err, ErrServerIdentityChanged) {
				t.Fatal("must not match unrelated sentinel")
			}
		})
	}
}

func TestUnreachableIsUnavailable(t *testing.T) {
	pki := newPKI(t)
	srv := pki.server(t, http.NotFoundHandler(), true)
	c := newClient(t, srv, pki.caFP)
	srv.Close()
	if err := c.ResolveAlert(context.Background(), "k", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadFile(t *testing.T) {
	pki := newPKI(t)
	var gotName, gotBody string
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		b := make([]byte, 64)
		n, _ := f.Read(b)
		gotName, gotBody = hdr.Filename, string(b[:n])
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(protocol.File{ID: "f1", Name: hdr.Filename})
	}), true)
	c := newClient(t, srv, pki.caFP)
	f, err := c.UploadFile(context.Background(), "a \"b\".txt", strings.NewReader("hello"))
	if err != nil || f.ID != "f1" {
		t.Fatalf("f=%v err=%v", f, err)
	}
	if gotBody != "hello" || gotName != `a "b".txt` {
		t.Fatalf("name=%q body=%q", gotName, gotBody)
	}
}

func TestRegisterCommandsAndRespond(t *testing.T) {
	pki := newPKI(t)
	var mu sync.Mutex
	var seen []string
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/api/v1/apps/self/commands" {
			var in []protocol.CommandInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in) != 1 || in[0].Name != "echo" {
				t.Errorf("body %v %v", in, err)
			}
		}
		_, _ = w.Write([]byte("{}"))
	}), true)
	c := newClient(t, srv, pki.caFP)
	ctx := context.Background()
	if err := c.RegisterCommands(ctx, []protocol.Command{{Name: "echo", Description: "d", Usage: "u", AppName: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Respond(ctx, "inv1", protocol.CommandResponse{Text: "hi", Ephemeral: true}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "PUT /api/v1/apps/self/commands" || seen[1] != "POST /api/v1/commands/invocations/inv1/respond" {
		t.Fatalf("seen %v", seen)
	}
}

func TestBackoffBounds(t *testing.T) {
	for n := 1; n <= 30; n++ {
		d := backoff(n)
		if d <= 0 || d > eventBackoffLimit {
			t.Fatalf("backoff(%d) = %v", n, d)
		}
	}
	if d := backoff(1); d < eventBackoffBase/2 || d > eventBackoffBase {
		t.Fatalf("backoff(1) = %v", d)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"": 0, "5": 5 * time.Second, "-3": 0, "junk": 0,
		now.Add(9 * time.Second).Format(http.TimeFormat): 9 * time.Second,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q = %v want %v", in, got, want)
		}
	}
}

func TestNilTransportWithPinUsesPinnedDefault(t *testing.T) {
	pki := newPKI(t)
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), true)
	c, err := New(Config{BaseURL: srv.URL, Token: "bub_test", CAFingerprint: pki.caFP, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.http.Get(srv.URL)
	if err != nil {
		t.Fatalf("pinned request: %v", err)
	}
	_ = resp.Body.Close()

	other := newPKI(t)
	bad, err := New(Config{BaseURL: srv.URL, Token: "bub_test", CAFingerprint: other.caFP, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatalf("New with wrong pin: %v", err)
	}
	if resp, err := bad.http.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("request succeeded against a wrong pin")
	}
}

// An IP-literal host is verified against the leaf's IP SAN: crypto/tls leaves
// ServerName empty for IPs, so a leaf for another address must be refused.
func TestPinnedTLSChecksIPSAN(t *testing.T) {
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(protocol.PostedAlert{Message: protocol.Message{ID: "m1"}})
	})
	wrong := newPKIFor(t, "10.9.8.7") // the test server listens on 127.0.0.1
	c := newClient(t, wrong.server(t, h, true), wrong.caFP)
	if _, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"}); !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("leaf without the dialed IP: err = %v, want ErrServerIdentityChanged", err)
	}
	if hits.Load() != 0 {
		t.Fatal("a request reached the server despite the SAN mismatch")
	}
	right := newPKIFor(t, "127.0.0.1")
	c = newClient(t, right.server(t, h, true), right.caFP)
	if _, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"}); err != nil {
		t.Fatalf("leaf with the dialed IP: %v", err)
	}
}
