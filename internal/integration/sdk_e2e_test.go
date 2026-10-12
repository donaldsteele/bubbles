package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

const adminPassword = "correct-horse-battery-staple-9"

// server is a full in-process bubbles server with an admin session.
type server struct {
	t       *testing.T
	baseURL string
	fp      string
	http    *http.Client
	cookie  *http.Cookie
}

func startServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: dir, HTTPSAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0", MDNSName: "bubbles", NoMDNS: true}
	a, err := app.New(context.Background(), cfg, nil, app.Options{
		Stderr:        io.Discard,
		AdminPassword: adminPassword,
		LANAddrs:      func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.1.20")}, nil },
		Hostname:      func() (string, error) { return "test-host", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("server did not stop")
		}
	})

	ca := a.Certs().CA()
	roots := x509.NewCertPool()
	roots.AddCert(ca.Cert)
	s := &server{
		t:       t,
		baseURL: "https://" + a.HTTPSAddr().String(),
		http:    &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}},
	}
	fp, err := bubblesclient.NormalizeFingerprint(ca.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	s.fp = fp

	res := s.rawDo("POST", "/api/v1/auth/login", protocol.LoginRequest{Username: "admin", Password: adminPassword})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("login: %d %s", res.StatusCode, b)
	}
	for _, c := range res.Cookies() {
		if strings.Contains(c.Name, "session") {
			s.cookie = c
		}
	}
	if s.cookie == nil {
		t.Fatal("login returned no session cookie")
	}
	name := s.rawDo("PATCH", "/api/v1/admin/settings", map[string]string{"instance_name": "Test Home"})
	name.Body.Close()
	if name.StatusCode != http.StatusOK {
		t.Fatalf("set instance name: %d", name.StatusCode)
	}
	return s
}

func (s *server) rawDo(method, path string, in any) *http.Response {
	s.t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			s.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.baseURL+path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Origin", s.baseURL)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.cookie != nil {
		req.AddCookie(s.cookie)
	}
	res, err := s.http.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	return res
}

// admin performs an admin API call and decodes a 2xx answer into out.
func (s *server) admin(method, path string, in, out any) {
	s.t.Helper()
	res := s.rawDo(method, path, in)
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		s.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			s.t.Fatalf("%s %s: decode: %v (%s)", method, path, err, b)
		}
	}
}

// pair runs request -> admin approve -> Wait and returns the app config.
func (s *server) pair(name string, scopes []string, createChannel bool) (bubblesclient.Config, protocol.PairingApproval) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := bubblesclient.RequestPairing(ctx, s.baseURL, s.fp, protocol.PairingRequest{AppName: name, RequestedScopes: scopes})
	if err != nil {
		s.t.Fatal(err)
	}
	if len(p.Code()) != 6 {
		s.t.Fatalf("code %q", p.Code())
	}
	var pending protocol.PendingPairingList
	s.admin("GET", "/api/v1/admin/pairing-requests", nil, &pending)
	var id string
	for _, it := range pending.Items {
		if it.Code == p.Code() {
			id = it.ID
			if bubblesclient.FormatFingerprint(it.CAFingerprint) != bubblesclient.FormatFingerprint(s.fp) {
				s.t.Fatalf("admin sees fingerprint %q, app pinned %q", it.CAFingerprint, s.fp)
			}
		}
	}
	if id == "" {
		s.t.Fatalf("pairing request with code %s not listed", p.Code())
	}
	var approval protocol.PairingApproval
	s.admin("POST", "/api/v1/admin/pairing-requests/"+id+"/approve",
		protocol.ApprovePairingRequest{CreateChannel: createChannel}, &approval)
	cfg, err := p.Wait(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	if cfg.Token == "" || cfg.CAFingerprint != s.fp {
		s.t.Fatalf("pairing config %+v", cfg)
	}
	return cfg, approval
}

func (s *server) client(cfg bubblesclient.Config) *bubblesclient.Client {
	s.t.Helper()
	c, err := bubblesclient.New(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return c
}

// tcpProxy forwards to target and can cut every live connection and refuse
// new ones, to simulate a server-side disconnect.
type tcpProxy struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	conns  []net.Conn
	refuse bool
}

func newProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln, target: target}
	t.Cleanup(func() { _ = ln.Close(); p.cut() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			refuse := p.refuse
			p.mu.Unlock()
			if refuse {
				_ = c.Close()
				continue
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, up)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
			go func() { _, _ = io.Copy(c, up); _ = c.Close() }()
		}
	}()
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func (p *tcpProxy) setRefuse(v bool) {
	p.mu.Lock()
	p.refuse = v
	p.mu.Unlock()
}

// next returns the next event matching pred, skipping others.
func next(t *testing.T, ch <-chan protocol.Event, pred func(protocol.Event) bool) protocol.Event {
	t.Helper()
	timeout := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed")
			}
			if pred(ev) {
				return ev
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}

func alertCreated(key string) func(protocol.Event) bool {
	return func(ev protocol.Event) bool {
		if ev.Type != "message.created" {
			return false
		}
		var m protocol.Message
		return json.Unmarshal(ev.Data, &m) == nil && m.Alert != nil && m.Alert.DedupeKey == key
	}
}

func TestSDKEndToEnd(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Sender app via pairing: request -> admin approves (creating its channel) -> Wait.
	senderCfg, approval := s.pair("netdiag", []string{protocol.ScopeAlertsWrite}, true)
	if len(approval.ChannelIDs) != 1 {
		t.Fatalf("channels %v", approval.ChannelIDs)
	}
	channelID := approval.ChannelIDs[0]
	sender := s.client(senderCfg)

	// Receiver app via the admin API; its bot joins the channel.
	var rapp protocol.App
	s.admin("POST", "/api/v1/admin/apps", protocol.CreateAppRequest{Name: "watcher"}, &rapp)
	var issued protocol.IssuedAppToken
	s.admin("POST", "/api/v1/admin/apps/"+rapp.ID+"/tokens", protocol.CreateAppTokenRequest{
		Scopes: []string{protocol.ScopeEventsRead, protocol.ScopeMessagesRead},
	}, &issued)
	s.admin("PUT", "/api/v1/channels/"+channelID+"/members/"+rapp.UserID, protocol.AddMemberRequest{}, nil)
	rcfg, err := bubblesclient.ParseConnectionString(issued.ConnectionString)
	if err != nil {
		t.Fatalf("connection string %q: %v", issued.ConnectionString, err)
	}
	if rcfg.CAFingerprint != s.fp {
		t.Fatalf("connection string pins %q, server CA is %q", rcfg.CAFingerprint, s.fp)
	}
	// The connection string names a LAN address; talk to loopback instead.
	rcfg.BaseURL = s.baseURL
	receiver := s.client(rcfg)

	if self, err := receiver.Self(ctx); err != nil || self.App.ID != rapp.ID {
		t.Fatalf("self: %v %v", self, err)
	}

	evCtx, evCancel := context.WithCancel(ctx)
	defer evCancel()
	events, err := receiver.Events(evCtx)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("post alert reaches second client, then resolves", func(t *testing.T) {
		msg, err := sender.PostAlert(ctx, protocol.AlertInput{
			Channel: channelID, Severity: "warning", Title: "Printer offline", Text: "no reply",
			DedupeKey: "printerOffline:10.0.0.9",
		})
		if err != nil || msg.Alert == nil || msg.Alert.State != "open" {
			t.Fatalf("PostAlert: %+v %v", msg, err)
		}
		ev := next(t, events, alertCreated("printerOffline:10.0.0.9"))
		var got protocol.Message
		if err := json.Unmarshal(ev.Data, &got); err != nil || got.ID != msg.ID || ev.ChannelID != channelID {
			t.Fatalf("event %+v / %+v", ev, got)
		}
		if err := sender.ResolveAlert(ctx, "printerOffline:10.0.0.9", "back online"); err != nil {
			t.Fatal(err)
		}
		ev = next(t, events, func(ev protocol.Event) bool {
			if ev.Type != "alert.updated" {
				return false
			}
			var u protocol.AlertUpdatedEvent
			return json.Unmarshal(ev.Data, &u) == nil && u.Transition == protocol.AlertTransitionResolved
		})
		var u protocol.AlertUpdatedEvent
		_ = json.Unmarshal(ev.Data, &u)
		if u.Message.Alert == nil || u.Message.Alert.State != "resolved" {
			t.Fatalf("resolved event %+v", u)
		}
	})

	t.Run("wrong fingerprint fails before any request", func(t *testing.T) {
		bad := senderCfg
		bad.CAFingerprint = "sha256:" + strings.Repeat("ab", 32)
		c := s.client(bad)
		_, err := c.PostAlert(ctx, protocol.AlertInput{Channel: channelID, Severity: "info", Title: "x", DedupeKey: "wrongfp"})
		if !errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			t.Fatalf("err = %v", err)
		}
		if _, err := c.Events(ctx); !errors.Is(err, bubblesclient.ErrServerIdentityChanged) {
			t.Fatalf("Events err = %v", err)
		}
		// The server never saw it: no alert with that key exists.
		var page protocol.AlertPage
		s.admin("GET", "/api/v1/alerts?channel_id="+channelID, nil, &page)
		for _, m := range page.Items {
			if m.Alert != nil && m.Alert.DedupeKey == "wrongfp" {
				t.Fatal("a request reached the server despite the pin mismatch")
			}
		}
	})

	t.Run("events resume without gaps after a forced disconnect", func(t *testing.T) {
		proxy := newProxy(t, strings.TrimPrefix(s.baseURL, "https://"))
		pcfg := rcfg
		pcfg.BaseURL = "https://" + proxy.addr()
		pc := s.client(pcfg)
		pctx, pcancel := context.WithCancel(ctx)
		defer pcancel()
		pev, err := pc.Events(pctx)
		if err != nil {
			t.Fatal(err)
		}
		post := func(key string) {
			if _, err := sender.PostAlert(ctx, protocol.AlertInput{Channel: channelID, Severity: "info", Title: key, DedupeKey: key}); err != nil {
				t.Fatal(err)
			}
		}
		post("gap-0")
		first := next(t, pev, alertCreated("gap-0"))

		proxy.setRefuse(true)
		proxy.cut()
		post("gap-1")
		post("gap-2")
		time.Sleep(300 * time.Millisecond)
		proxy.setRefuse(false)
		post("gap-3")

		var seqs []int64
		for _, key := range []string{"gap-1", "gap-2", "gap-3"} {
			ev := next(t, pev, alertCreated(key))
			seqs = append(seqs, ev.Seq)
		}
		for i, sq := range seqs {
			if sq <= first.Seq || (i > 0 && sq <= seqs[i-1]) {
				t.Fatalf("seqs not increasing after %d: %v", first.Seq, seqs)
			}
		}
		// No duplicate of anything already delivered follows.
		post("gap-4")
		ev := next(t, pev, func(ev protocol.Event) bool { return ev.Seq > 0 && ev.Type == "message.created" })
		if !alertCreated("gap-4")(ev) {
			t.Fatalf("expected gap-4 next, got seq %d type %s", ev.Seq, ev.Type)
		}
		if err := pc.EventsErr(); err != nil {
			t.Fatalf("EventsErr = %v", err)
		}
	})

	t.Run("revoked token stops the alert queue", func(t *testing.T) {
		q := bubblesclient.NewAlertQueue(sender, 10)
		defer q.Close()
		stopped := make(chan error, 1)
		q.SetOnStop(func(err error) { stopped <- err })
		if err := q.Enqueue(protocol.AlertInput{Channel: channelID, Severity: "info", Title: "before", DedupeKey: "q-before"}); err != nil {
			t.Fatal(err)
		}
		fctx, fcancel := context.WithTimeout(ctx, 20*time.Second)
		defer fcancel()
		if err := q.Flush(fctx); err != nil {
			t.Fatalf("flush before revoke: %v", err)
		}

		self, err := sender.Self(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.admin("DELETE", fmt.Sprintf("/api/v1/admin/apps/%s/tokens/%s", approval.App.ID, self.Token.ID), nil, nil)

		if _, err := sender.PostAlert(ctx, protocol.AlertInput{Channel: channelID, Severity: "info", Title: "x", DedupeKey: "revoked"}); !errors.Is(err, bubblesclient.ErrUnauthorized) {
			t.Fatalf("PostAlert err = %v", err)
		}
		if err := q.Enqueue(protocol.AlertInput{Channel: channelID, Severity: "info", Title: "after", DedupeKey: "q-after"}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-stopped:
			if !errors.Is(err, bubblesclient.ErrUnauthorized) {
				t.Fatalf("stop err = %v", err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("queue did not stop")
		}
		if !errors.Is(q.Err(), bubblesclient.ErrUnauthorized) {
			t.Fatalf("Err = %v", q.Err())
		}
		if _, err := sender.Events(ctx); !errors.Is(err, bubblesclient.ErrUnauthorized) {
			t.Fatalf("Events with revoked token = %v", err)
		}
	})
}

func TestSDKCommandsAndUpload(t *testing.T) {
	s := startServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cfg, approval := s.pair("echo", []string{protocol.ScopeCommands, protocol.ScopeEventsRead, protocol.ScopeMessagesWrite, protocol.ScopeFilesWrite}, true)
	bot := s.client(cfg)
	channelID := approval.ChannelIDs[0]

	if err := bot.RegisterCommands(ctx, []protocol.Command{{Name: "echo", Description: "repeat", Usage: "/echo <text>"}}); err != nil {
		t.Fatal(err)
	}
	events, err := bot.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	f, err := bot.UploadFile(ctx, "hello.txt", strings.NewReader("hello bubbles"))
	if err != nil || f.ID == "" {
		t.Fatalf("UploadFile: %+v %v", f, err)
	}
	msg, err := bot.PostMessage(ctx, channelID, protocol.NewMessage{Body: "with file", FileIDs: []string{f.ID}})
	if err != nil || len(msg.Files) != 1 {
		t.Fatalf("PostMessage: %+v %v", msg, err)
	}

	s.admin("POST", "/api/v1/commands/invoke", protocol.InvokeCommandRequest{ChannelID: channelID, Command: "echo", Args: "ping"}, nil)
	ev := next(t, events, func(ev protocol.Event) bool { return ev.Type == "command.invoked" })
	var inv protocol.CommandInvoked
	if err := json.Unmarshal(ev.Data, &inv); err != nil || inv.Args != "ping" {
		t.Fatalf("invoked %+v %v", inv, err)
	}
	if err := bot.Respond(ctx, inv.InvocationID, protocol.CommandResponse{Text: "ping", Ephemeral: true}); err != nil {
		t.Fatal(err)
	}
}
