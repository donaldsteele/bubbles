package bubblesclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestRequestPairingSendsReplacesAppID(t *testing.T) {
	pki := newPKI(t)
	var (
		mu   sync.Mutex
		body map[string]any
	)
	sent := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return body
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pairing/requests", func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		mu.Lock()
		body = raw
		mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(protocol.PairingCreated{ID: "p1", PollSecret: "sec", Code: "123456",
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)})
	})
	srv := pki.server(t, mux, true)
	p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{
		AppName: "netdiag", RequestedScopes: []string{protocol.ScopeAlertsWrite}, ReplacesAppID: "01JAPPNETDIAG0000000000000",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID() != "p1" {
		t.Errorf("ID = %q", p.ID())
	}
	if raw := sent(); raw["replaces_app_id"] != "01JAPPNETDIAG0000000000000" || raw["app_name"] != "netdiag" {
		t.Errorf("request body = %v", raw)
	}
	// A first pairing leaves the field out.
	if _, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{AppName: "netdiag"}); err != nil {
		t.Fatal(err)
	}
	if raw := sent(); raw == nil || raw["replaces_app_id"] != nil {
		t.Errorf("first pairing sent replaces_app_id: %v", raw)
	}
}

// TestRequestPairingFallsBackForAnOlderServer pairs against a fake of a
// bubbles v0.2.0 server, which decodes POST /pairing/requests with
// DisallowUnknownFields and so refuses replaces_app_id: RequestPairing sends
// the request once more without it. Any other 400 is returned as it is.
func TestRequestPairingFallsBackForAnOlderServer(t *testing.T) {
	pki := newPKI(t)
	var (
		mu     sync.Mutex
		bodies []string
		strict = true // false: answer like a current server that rejects the id
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pairing/requests", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		old := strict
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !old {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"one or more fields are invalid",` +
				`"details":{"fields":{"replaces_app_id":"replaces_app_id must be an app id"}}}}`))
			return
		}
		// The v0.2.0 request type, which has no replaces_app_id.
		var in struct {
			AppName         string   `json:"app_name"`
			Description     string   `json:"description,omitempty"`
			RequestedScopes []string `json:"requested_scopes,omitempty"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"bad_request","message":"one or more fields are invalid",` +
				`"details":{"fields":{"replaces_app_id":"unknown field"}}}}`))
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(protocol.PairingCreated{ID: "p1", PollSecret: "sec", Code: "123456",
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)})
	})
	srv := pki.server(t, mux, true)
	req := protocol.PairingRequest{AppName: "netdiag", RequestedScopes: []string{protocol.ScopeAlertsWrite}, ReplacesAppID: "01JAPPNETDIAG0000000000000"}
	p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, req)
	if err != nil {
		t.Fatalf("RequestPairing against v0.2.0: %v", err)
	}
	if p.ID() != "p1" {
		t.Errorf("ID = %q", p.ID())
	}
	mu.Lock()
	got := slices.Clone(bodies)
	bodies, strict = nil, false
	mu.Unlock()
	if len(got) != 2 || !strings.Contains(got[0], `"replaces_app_id"`) || strings.Contains(got[1], "replaces_app_id") ||
		!strings.Contains(got[1], `"app_name":"netdiag"`) {
		t.Errorf("requests sent = %q, want one with replaces_app_id, then one without", got)
	}

	// A current server's own refusal of the id is not retried.
	var ae *APIError
	if _, err := RequestPairing(context.Background(), srv.URL, pki.caFP, req); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("RequestPairing with a refused id = %v, want the 400", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Errorf("a refused id was sent %d times, want 1", len(bodies))
	}
}

func TestPairingCancel(t *testing.T) {
	pki := newPKI(t)
	var (
		mu        sync.Mutex
		cancelled bool
		claimed   bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pairing/requests", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(protocol.PairingCreated{ID: "p 1", PollSecret: "sec", Code: "123456",
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)})
	})
	authorized := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get(protocol.PairingHeader) != "sec" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(404)
			return false
		}
		return true
	}
	mux.HandleFunc("DELETE /api/v1/pairing/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "p 1" || !authorized(w, r) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if claimed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"this pairing request is claimed"}}`))
			return
		}
		cancelled = true
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/v1/pairing/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r) {
			return
		}
		mu.Lock()
		state := protocol.PairingPending
		if cancelled {
			state = protocol.PairingCancelled
		}
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(protocol.PairingStatus{State: state})
	})
	srv := pki.server(t, mux, true)
	p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
	if err != nil {
		t.Fatal(err)
	}
	p.poll = time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := p.Wait(context.Background())
		done <- err
	}()
	if err := p.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrPairingCancelled) {
			t.Fatalf("Wait after Cancel = %v, want ErrPairingCancelled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait did not end after Cancel")
	}
	// Cancelling twice is fine; a claimed request answers 409.
	if err := p.Cancel(context.Background()); err != nil {
		t.Errorf("second Cancel: %v", err)
	}
	mu.Lock()
	claimed = true
	mu.Unlock()
	var ae *APIError
	if err := p.Cancel(context.Background()); !errors.As(err, &ae) || ae.Status != 409 || ae.Code != protocol.CodeConflict {
		t.Errorf("Cancel of a claimed request = %v", err)
	}
}

func TestRevokeSelf(t *testing.T) {
	pki := newPKI(t)
	var (
		mu      sync.Mutex
		revoked bool
	)
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer bub_test" || revoked {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"sign in"}}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "DELETE /api/v1/apps/self/token":
			revoked = true
			w.WriteHeader(204)
		case "GET /api/v1/apps/self":
			_ = json.NewEncoder(w).Encode(protocol.AppSelf{App: protocol.App{ID: "a1"}})
		default:
			w.WriteHeader(404)
		}
	}), true)
	c := newClient(t, srv, pki.caFP)
	ctx := context.Background()
	if self, err := c.Self(ctx); err != nil || self.App.ID != "a1" {
		t.Fatalf("Self = %+v, %v", self, err)
	}
	if err := c.RevokeSelf(ctx); err != nil {
		t.Fatalf("RevokeSelf: %v", err)
	}
	if _, err := c.Self(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("Self after RevokeSelf = %v, want ErrUnauthorized", err)
	}
	if err := c.RevokeSelf(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("second RevokeSelf = %v, want ErrUnauthorized", err)
	}
}
