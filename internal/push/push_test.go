package push

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// ---- VAPID ----

func TestPushVAPIDIsCreatedOnceAndStaysPrivate(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateVAPID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.check(); err != nil {
		t.Fatalf("generated keys: %v", err)
	}
	pub, err := base64.RawURLEncoding.DecodeString(first.PublicKey)
	if err != nil || len(pub) != 65 || pub[0] != 4 {
		t.Errorf("public key is %d bytes (err %v), want a 65-byte uncompressed point", len(pub), err)
	}
	path := filepath.Join(dir, VAPIDFile)
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", VAPIDFile, fi.Mode().Perm())
		}
	}
	second, err := LoadOrCreateVAPID(dir)
	if err != nil || second != first {
		t.Fatalf("second load = %+v, %v; want the same keys", second, err)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(entries) != 0 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestPushVAPIDRejectsDamagedFiles(t *testing.T) {
	good, err := LoadOrCreateVAPID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := LoadOrCreateVAPID(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"not json":       `nope`,
		"unknown field":  `{"private_key":"` + good.PrivateKey + `","public_key":"` + good.PublicKey + `","extra":1}`,
		"mismatched":     `{"private_key":"` + good.PrivateKey + `","public_key":"` + other.PublicKey + `"}`,
		"empty keys":     `{"private_key":"","public_key":""}`,
		"bad private":    `{"private_key":"AAAA","public_key":"` + good.PublicKey + `"}`,
		"not base64url!": `{"private_key":"!!","public_key":"!!"}`,
	}
	for name, content := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, VAPIDFile), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateVAPID(dir); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
		// A damaged file is reported, never overwritten.
		if got, _ := os.ReadFile(filepath.Join(dir, VAPIDFile)); string(got) != content {
			t.Errorf("%s: the file was replaced", name)
		}
	}
}

// ---- endpoint allowlist (SSRF layer 1) ----

func TestPushValidateEndpoint(t *testing.T) {
	long := "https://fcm.googleapis.com/fcm/send/" + strings.Repeat("a", MaxEndpointLen)
	cases := []struct {
		name, endpoint string
		ok             bool
	}{
		{"fcm", "https://fcm.googleapis.com/fcm/send/abc:def", true},
		{"fcm explicit port", "https://fcm.googleapis.com:443/fcm/send/x", true},
		{"fcm empty port", "https://fcm.googleapis.com:/fcm/send/x", true},
		{"fcm upper case host", "https://FCM.GoogleAPIs.com/fcm/send/x", true},
		{"mozilla updates", "https://updates.push.services.mozilla.com/wpush/v2/gAAA", true},
		{"mozilla autopush subdomain", "https://autopush.push.services.mozilla.com/wpush/v2/x", true},
		{"apple web push", "https://web.push.apple.com/QAbc", true},
		{"apple subdomain", "https://api.push.apple.com/3/device/x", true},
		{"windows wns", "https://wns2-par02p.notify.windows.com/w/?token=abc", true},
		{"http scheme", "http://fcm.googleapis.com/fcm/send/x", false},
		{"no scheme", "fcm.googleapis.com/fcm/send/x", false},
		{"other scheme", "ftp://fcm.googleapis.com/x", false},
		{"empty", "", false},
		{"loopback ip", "https://127.0.0.1/push", false},
		{"loopback ip with port 443", "https://127.0.0.1:443/push", false},
		{"private ip", "https://10.0.0.5/push", false},
		{"private ip 192.168", "https://192.168.1.10:443/push", false},
		{"link local metadata ip", "https://169.254.169.254/latest/meta-data", false},
		{"ipv6 loopback", "https://[::1]/push", false},
		{"ipv6 unique local", "https://[fd00::1]/push", false},
		{"ipv4 mapped ipv6", "https://[::ffff:127.0.0.1]/push", false},
		{"decimal ip", "https://2130706433/push", false},
		{"localhost", "https://localhost/push", false},
		{"internal name", "https://nas.local/push", false},
		{"not allowlisted host", "https://example.com/push", false},
		{"lookalike suffix", "https://fcm.googleapis.com.evil.example/push", false},
		{"lookalike prefix", "https://evilfcm.googleapis.com/push", false},
		{"lookalike dash", "https://fcm-googleapis.com/push", false},
		{"suffix without label", "https://notify.windows.com/push", false},
		{"suffix without dot", "https://xnotify.windows.com.evil/push", false},
		{"bare apple suffix", "https://push.apple.com/x", false},
		{"userinfo", "https://user:pass@fcm.googleapis.com/fcm/send/x", false},
		{"userinfo name only", "https://user@fcm.googleapis.com/x", false},
		{"userinfo host trick", "https://fcm.googleapis.com@evil.example/x", false},
		{"backslash trick", `https://evil.example\@fcm.googleapis.com/x`, false},
		{"port 8443", "https://fcm.googleapis.com:8443/x", false},
		{"port 80", "https://fcm.googleapis.com:80/x", false},
		{"fragment", "https://fcm.googleapis.com/x#frag", false},
		{"trailing dot host", "https://fcm.googleapis.com./x", false},
		{"space", "https://fcm.googleapis.com /x", false},
		{"newline", "https://fcm.googleapis.com/x\n", false},
		{"opaque", "https:fcm.googleapis.com", false},
		{"too long", long, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEndpoint(tc.endpoint)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateEndpoint(%q) = %v, want ok=%v", tc.endpoint, err, tc.ok)
			}
			if err != nil && err.Error() == "" {
				t.Error("empty error text")
			}
		})
	}
}

// ---- address guard (SSRF layer 2) ----

func TestPushBlockedAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "0.1.2.3", "255.255.255.255", "224.0.0.1", "239.1.1.1",
		"240.0.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "198.18.0.1", "192.0.0.1",
		"::", "::1", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "2001:db8::1",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", "64:ff9b::7f00:1", "2002:7f00:1::1", "2001:0:4136:e378::1",
		"::8.8.8.8", "::808:808", "::ffff:8.8.8.8", "::ffff:0808:0808",
	}
	for _, s := range blocked {
		if !BlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("BlockedAddr(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "142.250.80.46", "17.253.144.10", "52.1.2.3", "2607:f8b0:4004:c07::5f", "2a00:1450:4001::1"}
	for _, s := range allowed {
		if BlockedAddr(netip.MustParseAddr(s)) {
			t.Errorf("BlockedAddr(%s) = true, want false", s)
		}
	}
	if !BlockedAddr(netip.Addr{}) {
		t.Error("the zero address must be blocked")
	}
}

type fakeResolver map[string][]netip.Addr

func (r fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := r[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

// TestPushDialerBlocksPublicNameResolvingToLoopback: a host name that looks
// public but resolves to 127.0.0.1 is refused before any connection is made.
func TestPushDialerBlocksPublicNameResolvingToLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	g := newGuardedDialer(fakeResolver{
		"push.example.net": {netip.MustParseAddr("127.0.0.1")},
		"rebind.example":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.7")},
		"v6.example":       {netip.MustParseAddr("::1")},
		"mapped.example":   {netip.MustParseAddr("::ffff:127.0.0.1")},
		"empty.example":    {},
	})
	for _, host := range []string{"push.example.net", "rebind.example", "v6.example", "mapped.example"} {
		conn, err := g.DialContext(context.Background(), "tcp", net.JoinHostPort(host, port))
		if conn != nil {
			conn.Close()
		}
		if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("dial %s: err = %v, want ErrBlockedAddress", host, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:" + port, "[::1]:" + port, "10.1.2.3:443", "169.254.169.254:80", "0.0.0.0:443"} {
		if conn, err := g.DialContext(context.Background(), "tcp", addr); !errors.Is(err, ErrBlockedAddress) {
			if conn != nil {
				conn.Close()
			}
			t.Errorf("dial %s: err = %v, want ErrBlockedAddress", addr, err)
		}
	}
	if _, err := g.DialContext(context.Background(), "tcp", "empty.example:443"); err == nil {
		t.Error("a name without addresses must not dial")
	}
	if _, err := g.DialContext(context.Background(), "tcp", "unknown.example:443"); err == nil {
		t.Error("an unresolvable name must not dial")
	}
	time.Sleep(50 * time.Millisecond)
	if n := accepted.Load(); n != 0 {
		t.Errorf("the loopback listener accepted %d connections, want 0", n)
	}
}

// TestPushDialerDialsTheValidatedAddress: a public answer is dialed by IP
// literal, so the name cannot resolve differently at connect time.
func TestPushDialerDialsTheValidatedAddress(t *testing.T) {
	var dialed []string
	g := newGuardedDialer(fakeResolver{"push.example.net": {netip.MustParseAddr("8.8.4.4"), netip.MustParseAddr("2607:f8b0:4004::1")}})
	g.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		dialed = append(dialed, network+" "+address)
		return nil, errors.New("test: no network")
	}
	_, err := g.DialContext(context.Background(), "tcp", "push.example.net:443")
	if err == nil || errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want the dial error", err)
	}
	want := []string{"tcp 8.8.4.4:443", "tcp [2607:f8b0:4004::1]:443"}
	if !slices.Equal(dialed, want) {
		t.Errorf("dialed %v, want %v", dialed, want)
	}
}

func TestPushClientDoesNotUseProxiesAndStopsAtRedirects(t *testing.T) {
	c := newClient(newGuardedDialer(nil).DialContext, nil, RequestTimeout)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatalf("transport = %#v: must have no proxy (a proxy bypasses the address guard)", c.Transport)
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Error("TLS 1.2 minimum missing")
	}
	if c.Timeout != RequestTimeout {
		t.Errorf("client timeout = %v, want %v", c.Timeout, RequestTimeout)
	}

	// Against a real (loopback test) server: the 307 is returned, not followed.
	srv := newPushServer(t, http.StatusTemporaryRedirect)
	plain := &net.Dialer{}
	cfg := srv.Client().Transport.(*http.Transport).TLSClientConfig
	client := newClient(plain.DialContext, cfg, RequestTimeout)
	req, _ := http.NewRequest(http.MethodPost, srv.endpoint("/start"), strings.NewReader("x"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status = %d, want the 307 itself", resp.StatusCode)
	}
	if n := len(srv.requests()); n != 1 {
		t.Errorf("server saw %d requests, want 1 (the redirect must not be followed)", n)
	}
}

// ---- subscriptions ----

func TestPushSubscribeUpsertsAndLimits(t *testing.T) {
	e := newEnv(t)
	s := e.service(nil)
	b := newBrowser(t)
	ep := func(n int) string { return "https://fcm.googleapis.com/fcm/send/dev" + string(rune('a'+n)) }
	in := func(n int, b *browser) SubscribeInput {
		return SubscribeInput{Endpoint: ep(n), P256DH: b.p256dh(), Auth: b.authKey(), UserAgent: "UA/1"}
	}

	if err := s.Subscribe(e.ctx, "u_alice", in(0, b)); err != nil {
		t.Fatal(err)
	}
	if !s.HasSubscriptions(e.ctx, "u_alice") || s.HasSubscriptions(e.ctx, "u_bob") {
		t.Error("HasSubscriptions wrong after the first subscribe")
	}
	// Same endpoint again: refreshed in place, failure count cleared.
	subs, _ := s.Subscriptions(e.ctx, "u_alice")
	e.failures(subs[0].ID, 3)
	b2 := newBrowser(t)
	if err := s.Subscribe(e.ctx, "u_alice", in(0, b2)); err != nil {
		t.Fatal(err)
	}
	subs, _ = s.Subscriptions(e.ctx, "u_alice")
	if len(subs) != 1 || subs[0].P256DH != b2.p256dh() || subs[0].Auth != b2.authKey() || subs[0].FailureCount != 0 || subs[0].UserAgent != "UA/1" {
		t.Fatalf("after upsert: %+v", subs)
	}

	// Up to the limit, then refused; an existing endpoint still refreshes.
	for n := 1; n < MaxSubscriptionsPerUser; n++ {
		if err := s.Subscribe(e.ctx, "u_alice", in(n, b)); err != nil {
			t.Fatalf("subscription %d: %v", n+1, err)
		}
	}
	if err := s.Subscribe(e.ctx, "u_alice", in(MaxSubscriptionsPerUser, b)); !errors.Is(err, ErrTooManySubscriptions) {
		t.Errorf("11th subscription: err = %v, want ErrTooManySubscriptions", err)
	}
	if err := s.Subscribe(e.ctx, "u_alice", in(3, b2)); err != nil {
		t.Errorf("refreshing an existing endpoint at the limit: %v", err)
	}
	if n := e.count("u_alice"); n != MaxSubscriptionsPerUser {
		t.Errorf("alice has %d subscriptions, want %d", n, MaxSubscriptionsPerUser)
	}

	// Only the owner removes a subscription; others' removals are no-ops.
	if err := s.Unsubscribe(e.ctx, "u_bob", ep(0)); err != nil {
		t.Fatal(err)
	}
	if n := e.count("u_alice"); n != MaxSubscriptionsPerUser {
		t.Errorf("bob removed alice's subscription (%d left)", n)
	}
	if err := s.Unsubscribe(e.ctx, "u_alice", ep(0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Unsubscribe(e.ctx, "u_alice", ep(0)); err != nil {
		t.Errorf("removing twice: %v", err)
	}
	if n := e.count("u_alice"); n != MaxSubscriptionsPerUser-1 {
		t.Errorf("alice has %d subscriptions after removing one, want %d", n, MaxSubscriptionsPerUser-1)
	}
}

func (e *env) failures(id string, n int) {
	e.t.Helper()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE push_subscriptions SET failure_count = ? WHERE id = ?`, n, id)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func TestPushSubscribeTakesOverAnEndpointForTheNewAccount(t *testing.T) {
	e := newEnv(t)
	s := e.service(nil)
	b := newBrowser(t)
	in := SubscribeInput{Endpoint: "https://updates.push.services.mozilla.com/wpush/v2/shared", P256DH: b.p256dh(), Auth: b.authKey()}
	if err := s.Subscribe(e.ctx, "u_alice", in); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe(e.ctx, "u_bob", in); err != nil {
		t.Fatal(err)
	}
	if e.count("u_alice") != 0 || e.count("u_bob") != 1 {
		t.Errorf("alice=%d bob=%d, want the endpoint to follow the latest account", e.count("u_alice"), e.count("u_bob"))
	}
}

func TestPushSubscribeTakeoverNeedsMatchingKeys(t *testing.T) {
	e := newEnv(t)
	s := e.service(nil)
	b := newBrowser(t)
	in := SubscribeInput{Endpoint: "https://updates.push.services.mozilla.com/wpush/v2/owned", P256DH: b.p256dh(), Auth: b.authKey()}
	if err := s.Subscribe(e.ctx, "u_alice", in); err != nil {
		t.Fatal(err)
	}
	other := newBrowser(t)
	for name, bad := range map[string]SubscribeInput{
		"other keys": {Endpoint: in.Endpoint, P256DH: other.p256dh(), Auth: other.authKey()},
		"other auth": {Endpoint: in.Endpoint, P256DH: b.p256dh(), Auth: other.authKey()},
		"other key":  {Endpoint: in.Endpoint, P256DH: other.p256dh(), Auth: b.authKey()},
	} {
		if err := s.Subscribe(e.ctx, "u_bob", bad); !errors.Is(err, ErrEndpointOwned) {
			t.Errorf("%s: err = %v, want ErrEndpointOwned", name, err)
		}
	}
	if e.count("u_alice") != 1 || e.count("u_bob") != 0 {
		t.Errorf("alice=%d bob=%d, the refused takeover changed ownership", e.count("u_alice"), e.count("u_bob"))
	}
	// The owner can still refresh with new keys.
	if err := s.Subscribe(e.ctx, "u_alice", SubscribeInput{Endpoint: in.Endpoint, P256DH: other.p256dh(), Auth: other.authKey()}); err != nil {
		t.Errorf("owner refresh: %v", err)
	}
}

func TestPushSubscribeValidatesInput(t *testing.T) {
	e := newEnv(t)
	s := e.service(nil)
	b := newBrowser(t)
	good := SubscribeInput{Endpoint: "https://fcm.googleapis.com/fcm/send/x", P256DH: b.p256dh(), Auth: b.authKey()}
	cases := []struct {
		name  string
		mod   func(*SubscribeInput)
		field string
	}{
		{"http endpoint", func(in *SubscribeInput) { in.Endpoint = "http://fcm.googleapis.com/x" }, "endpoint"},
		{"private ip endpoint", func(in *SubscribeInput) { in.Endpoint = "https://192.168.1.5/x" }, "endpoint"},
		{"loopback endpoint", func(in *SubscribeInput) { in.Endpoint = "https://127.0.0.1:443/x" }, "endpoint"},
		{"foreign host", func(in *SubscribeInput) { in.Endpoint = "https://example.com/x" }, "endpoint"},
		{"userinfo", func(in *SubscribeInput) { in.Endpoint = "https://u:p@fcm.googleapis.com/x" }, "endpoint"},
		{"empty endpoint", func(in *SubscribeInput) { in.Endpoint = "" }, "endpoint"},
		{"p256dh not base64", func(in *SubscribeInput) { in.P256DH = "***" }, "keys.p256dh"},
		{"p256dh wrong length", func(in *SubscribeInput) { in.P256DH = base64.RawURLEncoding.EncodeToString([]byte("short")) }, "keys.p256dh"},
		{"p256dh off curve", func(in *SubscribeInput) {
			bad := make([]byte, 65)
			bad[0] = 4
			in.P256DH = base64.RawURLEncoding.EncodeToString(bad)
		}, "keys.p256dh"},
		{"auth not base64", func(in *SubscribeInput) { in.Auth = "***" }, "keys.auth"},
		{"auth wrong length", func(in *SubscribeInput) { in.Auth = base64.RawURLEncoding.EncodeToString(make([]byte, 8)) }, "keys.auth"},
		{"auth empty", func(in *SubscribeInput) { in.Auth = "" }, "keys.auth"},
	}
	for _, tc := range cases {
		in := good
		tc.mod(&in)
		err := s.Subscribe(e.ctx, "u_alice", in)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Fields[tc.field] == "" {
			t.Errorf("%s: err = %v, want a ValidationError on %s", tc.name, err, tc.field)
		}
	}
	if n := e.count("u_alice"); n != 0 {
		t.Errorf("%d rows stored from invalid requests", n)
	}
	// Padded base64 and the standard alphabet are accepted and stored normalized.
	padded := good
	padded.P256DH = base64.URLEncoding.EncodeToString(b.priv.PublicKey().Bytes())
	padded.Auth = base64.StdEncoding.EncodeToString(b.auth)
	if err := s.Subscribe(e.ctx, "u_alice", padded); err != nil {
		t.Fatal(err)
	}
	subs, _ := s.Subscriptions(e.ctx, "u_alice")
	if len(subs) != 1 || subs[0].P256DH != b.p256dh() || subs[0].Auth != b.authKey() {
		t.Errorf("stored keys not normalized: %+v", subs)
	}
}

// ---- sending ----

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPushSendsEncryptedMinimalPayload(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client())
	b := newBrowser(t)
	id := e.insertSub("u_alice", srv.endpoint("/push/abc"), b)

	s.Send("u_alice", Notification{
		ChannelID: "c1", ChannelName: "ops", ChannelKind: "public", MessageID: "m1",
		SenderName: "Bob", Body: "the secret deploy password is hunter2",
	})
	srv.wait(t, 1)
	req := srv.requests()[0]
	if req.path != "/push/abc" {
		t.Errorf("path = %q", req.path)
	}
	h := req.header
	if h.Get("TTL") != "3600" || h.Get("Urgency") != "normal" || h.Get("Content-Encoding") != "aes128gcm" {
		t.Errorf("headers TTL=%q Urgency=%q Content-Encoding=%q", h.Get("TTL"), h.Get("Urgency"), h.Get("Content-Encoding"))
	}
	if auth := h.Get("Authorization"); !strings.HasPrefix(auth, "vapid t=") || !strings.HasSuffix(auth, ", k="+s.PublicKey()) {
		t.Errorf("Authorization = %q, want a VAPID header for the server key", auth)
	}

	plain := decrypt(t, b, req.body)
	var raw map[string]any
	if err := json.Unmarshal(plain, &raw); err != nil {
		t.Fatalf("payload %q: %v", plain, err)
	}
	if len(raw) != 3 || raw["title"] != "Bob in #ops" || raw["channel_id"] != "c1" || raw["message_id"] != "m1" {
		t.Errorf("minimal payload = %v, want exactly title, channel_id, message_id", raw)
	}
	if strings.Contains(string(plain), "hunter2") || strings.Contains(string(req.body), "hunter2") {
		t.Error("the message body leaked into a minimal payload")
	}

	waitFor(t, "last_ok_at", func() bool { return e.sub(id).lastOK != nil })
	if r := e.sub(id); !r.exists || r.failures != 0 || *r.lastOK != testNow.UnixMilli() {
		t.Errorf("subscription after success = %+v", r)
	}
}

func TestPushFullPayloadCarriesAnExcerptAndCriticalIsUrgent(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	e.set(settings.KeyPushPayload, settings.PushFull)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client())
	b := newBrowser(t)
	e.insertSub("u_alice", srv.endpoint("/p"), b)

	s.Send("u_alice", Notification{
		ChannelID: "c1", ChannelName: "ops", ChannelKind: "public", MessageID: "m2", SenderName: "Bob",
		Body: strings.Repeat("word ", 60), Critical: true,
	})
	srv.wait(t, 1)
	req := srv.requests()[0]
	if got := req.header.Get("Urgency"); got != "high" {
		t.Errorf("Urgency = %q, want high for a critical alert", got)
	}
	var p protocol.PushPayload
	if err := json.Unmarshal(decrypt(t, b, req.body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "Bob in #ops" || p.ChannelID != "c1" || p.MessageID != "m2" {
		t.Errorf("payload = %+v", p)
	}
	if n := len([]rune(p.Body)); n == 0 || n > MaxExcerptChars || !strings.HasSuffix(p.Body, "…") {
		t.Errorf("body = %q (%d chars), want a cut excerpt of at most %d", p.Body, n, MaxExcerptChars)
	}
}

func TestPushDirectMessageTitleIsJustTheSender(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client())
	b := newBrowser(t)
	e.insertSub("u_alice", srv.endpoint("/p"), b)
	s.Send("u_alice", Notification{ChannelID: "d1", ChannelKind: "dm", MessageID: "m3", SenderName: "Bob"})
	srv.wait(t, 1)
	var p protocol.PushPayload
	if err := json.Unmarshal(decrypt(t, b, srv.requests()[0].body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "Bob" {
		t.Errorf("title = %q, want Bob", p.Title)
	}
}

func TestPushSendsToEveryDeviceOfTheUserOnly(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client())
	b1, b2, b3 := newBrowser(t), newBrowser(t), newBrowser(t)
	e.insertSub("u_alice", srv.endpoint("/phone"), b1)
	e.insertSub("u_alice", srv.endpoint("/laptop"), b2)
	e.insertSub("u_bob", srv.endpoint("/bobs"), b3)
	s.Send("u_alice", Notification{ChannelID: "c", ChannelName: "x", ChannelKind: "public", MessageID: "m", SenderName: "S"})
	srv.wait(t, 2)
	time.Sleep(50 * time.Millisecond)
	paths := []string{}
	for _, r := range srv.requests() {
		paths = append(paths, r.path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"/laptop", "/phone"}) {
		t.Errorf("requests went to %v, want alice's two devices", paths)
	}
}

func TestPushDisabledSettingSendsNothing(t *testing.T) {
	e := newEnv(t)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client())
	e.insertSub("u_alice", srv.endpoint("/p"), newBrowser(t))
	s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
	if n := len(srv.requests()); n != 0 {
		t.Errorf("%d requests while web_push_enabled is off", n)
	}
}

func TestPushGoneSubscriptionIsDeleted(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusNotFound} {
		e := newEnv(t)
		e.set(settings.KeyWebPushEnabled, true)
		srv := newPushServer(t, status)
		s := e.service(srv.Client())
		gone := e.insertSub("u_alice", srv.endpoint("/gone"), newBrowser(t))
		other := e.insertSub("u_bob", srv.endpoint("/other"), newBrowser(t))
		s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
		if e.sub(gone).exists {
			t.Errorf("status %d: the subscription survived", status)
		}
		if !e.sub(other).exists {
			t.Errorf("status %d: another user's subscription was deleted", status)
		}
	}
}

func TestPushFiveConsecutiveFailuresDeleteTheSubscription(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusInternalServerError)
	s := e.service(srv.Client())
	id := e.insertSub("u_alice", srv.endpoint("/flaky"), newBrowser(t))
	send := func() {
		s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
	}

	for i := 1; i <= 2; i++ {
		send()
		if r := e.sub(id); !r.exists || r.failures != i {
			t.Fatalf("after %d failures: %+v", i, r)
		}
	}
	// A success resets the streak.
	srv.setStatus(http.StatusCreated)
	send()
	if r := e.sub(id); !r.exists || r.failures != 0 || r.lastOK == nil {
		t.Fatalf("after a success: %+v", r)
	}
	srv.setStatus(http.StatusTooManyRequests)
	for i := 1; i < MaxFailures; i++ {
		send()
		if r := e.sub(id); !r.exists || r.failures != i {
			t.Fatalf("after %d failures: %+v", i, r)
		}
	}
	send()
	if e.sub(id).exists {
		t.Error("the subscription survived its 5th consecutive failure")
	}
}

func TestPushRedirectAnswerIsAFailureNotAHop(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusTemporaryRedirect)
	client := newClient((&net.Dialer{}).DialContext, srv.Client().Transport.(*http.Transport).TLSClientConfig, RequestTimeout)
	s := e.service(client)
	id := e.insertSub("u_alice", srv.endpoint("/r"), newBrowser(t))
	s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
	if n := len(srv.requests()); n != 1 {
		t.Errorf("push service saw %d requests, want 1", n)
	}
	if r := e.sub(id); !r.exists || r.failures != 1 {
		t.Errorf("subscription after a redirect = %+v, want one failure", r)
	}
}

func TestPushNetworkErrorCountsAsAFailure(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusCreated)
	ep := srv.endpoint("/down")
	srv.Close()
	s := e.service(srv.Client())
	id := e.insertSub("u_alice", ep, newBrowser(t))
	s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
	if r := e.sub(id); !r.exists || r.failures != 1 {
		t.Errorf("subscription after a connection error = %+v, want one failure", r)
	}
}

// TestPushUnlistedStoredEndpointIsNeverContacted: a row written around the API
// (or before the allowlist) is dropped without a request.
func TestPushUnlistedStoredEndpointIsNeverContacted(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	var calls atomic.Int32
	s, err := New(Options{DB: e.db, Settings: e.st, DataDir: e.dataDir, client: countingClient{&calls}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ep := range []string{"http://127.0.0.1:8080/x", "https://10.0.0.5/x", "https://example.com/x", "https://u:p@fcm.googleapis.com/x"} {
		id := e.insertSub("u_alice", ep, newBrowser(t))
		s.deliver(e.ctx, job{userID: "u_alice", n: Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}})
		if e.sub(id).exists {
			t.Errorf("subscription %q survived", ep)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("%d requests were made to unlisted endpoints", n)
	}
}

type countingClient struct{ n *atomic.Int32 }

func (c countingClient) Do(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, errors.New("test client must not be used")
}

// blockingClient parks every request until released.
type blockingClient struct {
	started chan struct{}
	release chan struct{}
}

func (c blockingClient) Do(req *http.Request) (*http.Response, error) {
	c.started <- struct{}{}
	select {
	case <-c.release:
	case <-req.Context().Done():
	}
	return nil, errors.New("released")
}

func TestPushSendNeverBlocksAndDropsWhenTheQueueIsFull(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	bc := blockingClient{started: make(chan struct{}, 8), release: make(chan struct{})}
	s, err := New(Options{DB: e.db, Settings: e.st, DataDir: e.dataDir, Workers: 1, QueueSize: 2, client: bc, allowAnyEndpoint: true})
	if err != nil {
		t.Fatal(err)
	}
	e.insertSub("u_alice", "https://push.invalid/x", newBrowser(t))
	n := Notification{ChannelID: "c", MessageID: "m", SenderName: "S"}

	s.Send("u_alice", n)
	<-bc.started // the worker holds job 1
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Send("u_alice", n) // queued
		s.Send("u_alice", n) // queued
		s.Send("u_alice", n) // dropped
		s.Send("u_alice", n) // dropped
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a full queue")
	}
	if got := s.Dropped(); got != 2 {
		t.Errorf("Dropped = %d, want 2", got)
	}
	close(bc.release)
	s.Close()
	s.Close() // idempotent
	s.Send("u_alice", n)
}

func TestPushWorkersRunConcurrently(t *testing.T) {
	e := newEnv(t)
	e.set(settings.KeyWebPushEnabled, true)
	srv := newPushServer(t, http.StatusCreated)
	s := e.service(srv.Client(), func(o *Options) { o.Workers = 4 })
	for i := range 6 {
		user := "u_alice"
		if i%2 == 1 {
			user = "u_bob"
		}
		e.insertSub(user, srv.endpoint("/d"+string(rune('0'+i))), newBrowser(t))
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Send("u_alice", Notification{ChannelID: "c", ChannelName: "x", ChannelKind: "public", MessageID: "m", SenderName: "S"})
			s.Send("u_bob", Notification{ChannelID: "c", ChannelName: "x", ChannelKind: "public", MessageID: "m", SenderName: "S"})
		}()
	}
	wg.Wait()
	srv.wait(t, 18)
}

// ---- payload ----

func TestPushTitleAndExcerpt(t *testing.T) {
	titles := []struct {
		n    Notification
		want string
	}{
		{Notification{SenderName: "Ann", ChannelName: "general", ChannelKind: "public"}, "Ann in #general"},
		{Notification{SenderName: "Ann", ChannelName: "vault", ChannelKind: "private"}, "Ann in #vault"},
		{Notification{SenderName: "Ann", ChannelKind: "dm"}, "Ann"},
		{Notification{SenderName: "Ann", ChannelKind: "group_dm", ChannelName: "ignored"}, "Ann"},
		{Notification{SenderName: " A\n\tnn ", ChannelName: "x", ChannelKind: "public"}, "A nn in #x"},
		{Notification{ChannelName: "x", ChannelKind: "public"}, "Someone in #x"},
	}
	for _, tc := range titles {
		if got := Title(tc.n); got != tc.want {
			t.Errorf("Title(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
	long := strings.Repeat("é", 300)
	excerpts := []struct{ in, want string }{
		{"hello", "hello"},
		{"  hello \n\n world\t!  ", "hello world !"},
		{"a\x00b\x07c", "abc"},
		{"", ""},
		{strings.Repeat("x", 120), strings.Repeat("x", 120)},
		{strings.Repeat("x", 121), strings.Repeat("x", 119) + "…"},
		{long, strings.Repeat("é", 119) + "…"},
	}
	for _, tc := range excerpts {
		got := Excerpt(tc.in)
		if got != tc.want || len([]rune(got)) > MaxExcerptChars {
			t.Errorf("Excerpt(%.20q…) = %q (%d chars), want %q", tc.in, got, len([]rune(got)), tc.want)
		}
	}
}

func TestPushPayloadModes(t *testing.T) {
	n := Notification{ChannelID: "c1", ChannelName: "ops", ChannelKind: "public", MessageID: "m1", SenderName: "Bob", Body: "top secret"}
	min, err := BuildPayload(n, settings.PushMinimal)
	if err != nil {
		t.Fatal(err)
	}
	if string(min) != `{"title":"Bob in #ops","channel_id":"c1","message_id":"m1"}` {
		t.Errorf("minimal payload = %s", min)
	}
	full, _ := BuildPayload(n, settings.PushFull)
	if string(full) != `{"title":"Bob in #ops","channel_id":"c1","message_id":"m1","body":"top secret"}` {
		t.Errorf("full payload = %s", full)
	}
	// Anything but "full" is treated as minimal.
	if odd, _ := BuildPayload(n, "verbose"); strings.Contains(string(odd), "secret") {
		t.Errorf("unknown mode leaked the body: %s", odd)
	}
}
