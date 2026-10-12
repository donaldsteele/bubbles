package httpapi

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"

	"github.com/donaldsteele/bubbles/internal/push"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Every exchange here goes through the contract harness: requests and
// responses are validated against docs/openapi.yaml as a side effect.

const (
	pushKeyPath  = "/api/v1/push/vapid-public-key"
	pushSubsPath = "/api/v1/push/subscriptions"
	fcmEndpoint  = "https://fcm.googleapis.com/fcm/send/device-1"
)

// browserKeys are the keys a browser's PushSubscription would carry.
func browserKeys(t *testing.T) protocol.PushKeys {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return protocol.PushKeys{
		P256DH: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth),
	}
}

func (e *env) pushRows(userID string) int {
	return e.count(`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = ?`, userID)
}

func TestPushRoutesRequireSignIn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	anon := e.client("")
	wantStatus(t, anon.do(http.MethodGet, pushKeyPath, nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, anon.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: fcmEndpoint, Keys: browserKeys(t)}),
		http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, anon.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: fcmEndpoint}),
		http.StatusUnauthorized, CodeUnauthorized)
}

func TestPushVapidKeyIsNotFoundWhileDisabled(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()

	// web_push_enabled defaults to false.
	rec := alice.do(http.MethodGet, pushKeyPath, nil)
	wantStatus(t, rec, http.StatusNotFound, CodeNotFound)

	e.set(t, settings.KeyWebPushEnabled, true)
	rec = alice.do(http.MethodGet, pushKeyPath, nil)
	wantStatus(t, rec, http.StatusOK, "")
	key := decodeBody[protocol.PushVapidKey](t, rec).Key
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		t.Fatalf("key %q is %d bytes (err %v), want a 65-byte uncompressed P-256 point", key, len(raw), err)
	}
	if key != e.push.PublicKey() {
		t.Error("the endpoint does not serve the server's VAPID public key")
	}

	e.set(t, settings.KeyWebPushEnabled, false)
	wantStatus(t, alice.do(http.MethodGet, pushKeyPath, nil), http.StatusNotFound, CodeNotFound)
}

func TestPushSubscribeUpsertsAndUnsubscribesOnlyOwnRows(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")

	req := protocol.PushSubscribeRequest{Endpoint: fcmEndpoint, Keys: browserKeys(t)}
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, req), http.StatusNoContent, "")
	if n := e.pushRows(alice.id); n != 1 {
		t.Fatalf("alice has %d rows, want 1", n)
	}
	var ua string
	if err := e.db.Read().QueryRow(`SELECT user_agent FROM push_subscriptions WHERE user_id = ?`, alice.id).Scan(&ua); err != nil || ua != "test-agent" {
		t.Errorf("user_agent = %q (%v), want the request's", ua, err)
	}

	// Same endpoint, fresh keys: refreshed, not duplicated.
	req.Keys = browserKeys(t)
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, req), http.StatusNoContent, "")
	var p256dh string
	if err := e.db.Read().QueryRow(`SELECT p256dh FROM push_subscriptions WHERE user_id = ?`, alice.id).Scan(&p256dh); err != nil || p256dh != req.Keys.P256DH {
		t.Errorf("stored p256dh = %q (%v), want the refreshed key", p256dh, err)
	}
	if n := e.pushRows(alice.id); n != 1 {
		t.Fatalf("alice has %d rows after the upsert, want 1", n)
	}

	// Bob's own device; bob cannot remove alice's.
	other := "https://updates.push.services.mozilla.com/wpush/v2/bobs"
	wantStatus(t, bob.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: other, Keys: browserKeys(t)}), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: fcmEndpoint}), http.StatusNoContent, "")
	if e.pushRows(alice.id) != 1 || e.pushRows(bob.id) != 1 {
		t.Fatalf("alice=%d bob=%d rows after bob's delete of alice's endpoint, want 1 and 1", e.pushRows(alice.id), e.pushRows(bob.id))
	}

	// Alice removes hers (twice: idempotent) and an endpoint she never had.
	wantStatus(t, alice.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: fcmEndpoint}), http.StatusNoContent, "")
	wantStatus(t, alice.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: fcmEndpoint}), http.StatusNoContent, "")
	wantStatus(t, alice.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: "https://never.example/x"}), http.StatusNoContent, "")
	if e.pushRows(alice.id) != 0 || e.pushRows(bob.id) != 1 {
		t.Errorf("alice=%d bob=%d rows at the end, want 0 and 1", e.pushRows(alice.id), e.pushRows(bob.id))
	}
}

// TestPushSubscribeRefusesUnsafeEndpoints is the SSRF check at the API: the
// server will POST to this URL, so only https URLs of browser push services
// are accepted.
func TestPushSubscribeRefusesUnsafeEndpoints(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	cases := map[string]string{
		"http scheme":               "http://fcm.googleapis.com/fcm/send/x",
		"private ip literal":        "https://192.168.1.10/push",
		"private ip, port 443":      "https://10.0.0.5:443/push",
		"loopback ip literal":       "https://127.0.0.1/push",
		"ipv6 loopback":             "https://[::1]/push",
		"link-local metadata":       "https://169.254.169.254/latest/meta-data/",
		"public ip literal":         "https://8.8.8.8/push",
		"localhost":                 "https://localhost/push",
		"host not on the list":      "https://example.com/push",
		"lookalike host":            "https://fcm.googleapis.com.evil.example/push",
		"userinfo":                  "https://user:secret@fcm.googleapis.com/fcm/send/x",
		"userinfo host trick":       "https://fcm.googleapis.com@evil.example/push",
		"non-443 port":              "https://fcm.googleapis.com:8443/push",
		"no scheme":                 "fcm.googleapis.com/fcm/send/x",
		"empty":                     "",
		"too long":                  "https://fcm.googleapis.com/" + repeat("a", push.MaxEndpointLen),
		"fragment":                  "https://fcm.googleapis.com/x#y",
		"file scheme":               "file:///etc/passwd",
		"wss scheme":                "wss://fcm.googleapis.com/x",
		"windows notify, bare apex": "https://notify.windows.com/x",
	}
	for name, endpoint := range cases {
		rec := alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint, Keys: browserKeys(t)})
		if endpoint == "" || len(endpoint) > 2048 {
			// Rejected by the request schema or the server; either way a 400.
			wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
			continue
		}
		perr := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
		if perr.FieldErrors()["endpoint"] == "" {
			t.Errorf("%s: details.fields = %v, want a reason for endpoint", name, perr.FieldErrors())
		}
	}
	if n := e.pushRows(alice.id); n != 0 {
		t.Errorf("%d subscriptions stored from rejected endpoints", n)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}

func TestPushSubscribeValidatesKeysAndBody(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	good := browserKeys(t)

	cases := []struct {
		name  string
		keys  protocol.PushKeys
		field string
	}{
		{"p256dh not base64", protocol.PushKeys{P256DH: "***", Auth: good.Auth}, "keys.p256dh"},
		{"p256dh not a curve point", protocol.PushKeys{P256DH: base64.RawURLEncoding.EncodeToString(make([]byte, 65)), Auth: good.Auth}, "keys.p256dh"},
		{"auth wrong length", protocol.PushKeys{P256DH: good.P256DH, Auth: base64.RawURLEncoding.EncodeToString([]byte("short"))}, "keys.auth"},
		{"auth not base64", protocol.PushKeys{P256DH: good.P256DH, Auth: "***"}, "keys.auth"},
	}
	for _, tc := range cases {
		rec := alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: fcmEndpoint, Keys: tc.keys})
		perr := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
		if perr.FieldErrors()[tc.field] == "" {
			t.Errorf("%s: details.fields = %v, want %s", tc.name, perr.FieldErrors(), tc.field)
		}
	}
	// Unknown fields (notably a user_id) are refused, so a subscription can
	// only ever be for the caller.
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath,
		`{"endpoint":"`+fcmEndpoint+`","keys":{"p256dh":"`+good.P256DH+`","auth":"`+good.Auth+`"},"user_id":"someone-else"}`),
		http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, `{"endpoint":"`+fcmEndpoint+`"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, `not json`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, `x`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }),
		http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	if n := e.pushRows(alice.id); n != 0 {
		t.Errorf("%d rows stored from rejected requests", n)
	}
}

func TestPushSubscribeLimitsEachUserToTenDevices(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "192.168.1.51")
	endpoint := func(i int) string { return "https://fcm.googleapis.com/fcm/send/device-" + strconv.Itoa(i) }
	for i := range push.MaxSubscriptionsPerUser {
		wantStatus(t, alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint(i), Keys: browserKeys(t)}), http.StatusNoContent, "")
	}
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint(99), Keys: browserKeys(t)}),
		http.StatusConflict, CodeConflict)
	// A known endpoint can still be refreshed, and other users have their own quota.
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint(3), Keys: browserKeys(t)}), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint(99), Keys: browserKeys(t)}), http.StatusNoContent, "")
	if e.pushRows(alice.id) != push.MaxSubscriptionsPerUser || e.pushRows(bob.id) != 1 {
		t.Errorf("alice=%d bob=%d rows", e.pushRows(alice.id), e.pushRows(bob.id))
	}
	// Making room lets the next subscription in.
	wantStatus(t, alice.do(http.MethodDelete, pushSubsPath, protocol.PushUnsubscribeRequest{Endpoint: endpoint(0)}), http.StatusNoContent, "")
	wantStatus(t, alice.do(http.MethodPost, pushSubsPath, protocol.PushSubscribeRequest{Endpoint: endpoint(98), Keys: browserKeys(t)}), http.StatusNoContent, "")
}
