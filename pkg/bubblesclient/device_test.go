package bubblesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestDeviceClientCalls(t *testing.T) {
	pki := newPKI(t)
	var gotAuth []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/device-login", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.DeviceLoginRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Password != "right" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid username or password"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.DeviceLoginResponse{DeviceToken: "bub_dev", DeviceID: "d1", HandoffCode: "h1"})
	})
	mux.HandleFunc("POST /api/v1/auth/handoff-code", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(protocol.HandoffCodeResponse{HandoffCode: "h2"})
	})
	mux.HandleFunc("DELETE /api/v1/me/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "d 1" {
			t.Errorf("id = %q", r.PathValue("id"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := pki.server(t, mux, true)
	anon, err := New(Config{BaseURL: srv.URL, CAFingerprint: pki.caFP})
	if err != nil {
		t.Fatal(err)
	}
	res, err := anon.DeviceLogin(context.Background(), "alice", "right", "laptop")
	if err != nil || res.DeviceToken != "bub_dev" || res.HandoffCode != "h1" {
		t.Fatalf("DeviceLogin = %+v, %v", res, err)
	}
	if _, err := anon.DeviceLogin(context.Background(), "alice", "wrong", "laptop"); err == nil {
		t.Fatal("wrong password accepted")
	} else {
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
			t.Fatalf("err = %v", err)
		}
	}

	dev, _ := New(Config{BaseURL: srv.URL, Token: "bub_dev", CAFingerprint: pki.caFP})
	code, err := dev.HandoffCode(context.Background())
	if err != nil || code != "h2" || gotAuth[0] != "Bearer bub_dev" {
		t.Fatalf("HandoffCode = %q, %v, auth %v", code, err, gotAuth)
	}
	if u := dev.HandoffURL("a b&c"); !strings.HasSuffix(u, "/auth/handoff?code=a+b%26c") || !strings.HasPrefix(u, "https://") {
		t.Fatalf("HandoffURL = %q", u)
	}
	if u := dev.HandoffURLNext("c1", "/c/01ABC?x=1&y=2"); !strings.HasSuffix(u, "/auth/handoff?code=c1&next=%2Fc%2F01ABC%3Fx%3D1%26y%3D2") {
		t.Fatalf("HandoffURLNext = %q", u)
	}
	if u := dev.HandoffURLNext("c1", ""); strings.Contains(u, "next=") {
		t.Fatalf("HandoffURLNext with no next = %q", u)
	}
	if err := dev.RevokeDevice(context.Background(), "d 1"); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
}

func TestDeviceRegisterAndStatus(t *testing.T) {
	pki := newPKI(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/device-register", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.DeviceRegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Name == "taken" {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"that name is already taken"}}`))
			return
		}
		if req.Name != "Ada" || req.Password != "pw" || req.DeviceName != "laptop" {
			t.Errorf("request = %+v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(protocol.DeviceLoginResponse{DeviceToken: "bub_dev", DeviceID: "d1", HandoffCode: "h1"})
	})
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("status sent a token")
		}
		closes := "2026-01-02T03:04:05.000Z"
		_ = json.NewEncoder(w).Encode(protocol.ServerStatus{InstanceName: "home", RegistrationOpen: true, RegistrationClosesAt: &closes})
	})
	srv := pki.server(t, mux, true)
	c, err := New(Config{BaseURL: srv.URL, CAFingerprint: pki.caFP})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.DeviceRegister(context.Background(), "Ada", "pw", "laptop")
	if err != nil || res.DeviceToken != "bub_dev" || res.HandoffCode != "h1" {
		t.Fatalf("DeviceRegister = %+v, %v", res, err)
	}
	var ae *APIError
	if _, err := c.DeviceRegister(context.Background(), "taken", "pw", "laptop"); !errors.As(err, &ae) || ae.Status != http.StatusConflict {
		t.Fatalf("taken name: %v", err)
	}
	st, err := c.Status(context.Background())
	if err != nil || st.InstanceName != "home" || !st.RegistrationOpen || st.RegistrationClosesAt == nil {
		t.Fatalf("Status = %+v, %v", st, err)
	}
}
