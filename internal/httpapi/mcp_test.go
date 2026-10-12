package httpapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/donaldsteele/bubbles/internal/mcpserver"
	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// mcpPKI is a CA and a leaf for 127.0.0.1.
type mcpPKI struct {
	pool  *x509.CertPool
	fp    string
	chain tls.Certificate
}

func newMCPPKI(t *testing.T) *mcpPKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(caDER)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &mcpPKI{pool: pool, fp: "sha256:" + hex.EncodeToString(sum[:]),
		chain: tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}
}

// bearerTripper adds the token and keeps the fake clock moving so the
// per-token rate limit never gets in the way of a test.
type bearerTripper struct {
	e     *env
	token string
	base  http.RoundTripper
}

func (b bearerTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	b.e.clock.Advance(time.Second)
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

type mcpFixture struct {
	e       *env
	srv     *httptest.Server
	pki     *mcpPKI
	admin   member
	alice   member
	ops     protocol.Channel
	secret  protocol.Channel
	full    bot // mcp + messages:read/write + alerts:write, member of #ops
	readBot bot // mcp + messages:read, member of #ops
	noMCP   bot // messages:read only
	hello   protocol.Message
}

func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	e := newEnv(t)
	f := &mcpFixture{e: e, pki: newMCPPKI(t)}
	f.admin = e.newAdmin()
	f.alice = e.newMember("alice", "192.168.1.20")
	f.ops = f.admin.createChannel(t, "ops", "public", f.alice)
	f.secret = f.admin.createChannel(t, "secret", "private", f.alice)
	f.hello = f.alice.post(t, f.ops.ID, "disk report for the cluster")
	f.alice.post(t, f.secret.ID, "launch codes are secret")
	f.full = e.newBot(f.admin, "agent", []string{"mcp", "messages:read", "messages:write", "alerts:write"}, nil, f.ops.ID)
	f.readBot = e.newBot(f.admin, "reader", []string{"mcp", "messages:read"}, nil, f.ops.ID)
	f.noMCP = e.newBot(f.admin, "nomcp", []string{"messages:read", "messages:write", "alerts:write"}, nil, f.ops.ID)

	srv := httptest.NewUnstartedServer(e.raw)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{f.pki.chain}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	f.srv = srv
	return f
}

func (f *mcpFixture) httpClient(token string) *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.pki.pool}}
	return &http.Client{Transport: bearerTripper{e: f.e, token: token, base: tr}}
}

// local connects an MCP client to the HTTP endpoint (local backend).
func (f *mcpFixture) local(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.srv.URL + "/api/v1/mcp", HTTPClient: f.httpClient(token),
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// remote connects an MCP client to a server backed by the REST client
// (remote backend) over in-memory transports, pinning the test CA.
func (f *mcpFixture) remote(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	c, err := bubblesclient.New(bubblesclient.Config{BaseURL: f.srv.URL, Token: token, CAFingerprint: f.pki.fp})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := mcpserver.New(mcpserver.NewRemote(c), mcpserver.Info{}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %v", resultText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("structured content %s: %v", b, err)
	}
	return m
}

func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (f *mcpFixture) alertArgs() map[string]any {
	return map[string]any{"channel": "#ops", "severity": "warning", "title": "Disk almost full", "text": "92% used", "dedupe_key": "disk"}
}

func TestMCPToolsListIsExactlyTheEightTools(t *testing.T) {
	f := newMCPFixture(t)
	want := []string{"ack_alert", "list_alerts", "list_channels", "post_alert", "post_message", "read_messages", "resolve_alert", "search_messages"}
	for name, cs := range map[string]*mcp.ClientSession{"local": f.local(t, f.full.token), "remote": f.remote(t, f.full.token)} {
		res, err := cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(name, err)
		}
		var got []string
		for _, tl := range res.Tools {
			got = append(got, tl.Name)
			if tl.Description == "" || tl.InputSchema == nil {
				t.Errorf("%s: tool %s has no description or schema", name, tl.Name)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s: tools = %v, want %v", name, got, want)
		}
	}
}

func TestMCPEndpointAuthentication(t *testing.T) {
	f := newMCPFixture(t)
	e := f.e
	init := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	do := func(mods ...func(*http.Request)) *httptest.ResponseRecorder {
		return e.client("192.168.1.61").do(http.MethodPost, "/api/v1/mcp", init, append([]func(*http.Request){func(r *http.Request) {
			r.Header.Set("Accept", "application/json, text/event-stream")
		}}, mods...)...)
	}
	// Browser session (a cookie): refused with 403.
	rec := f.admin.do(http.MethodPost, "/api/v1/mcp", init, func(r *http.Request) {
		r.Header.Set("Accept", "application/json, text/event-stream")
	})
	wantStatus(t, rec, http.StatusForbidden, "forbidden")
	// A token without the mcp scope: 403.
	rec = do(asBearer(f.noMCP.token))
	wantStatus(t, rec, http.StatusForbidden, "forbidden")
	if !strings.Contains(rec.Body.String(), "mcp scope") {
		t.Errorf("body = %s", rec.Body.String())
	}
	// No credentials: 401.
	wantStatus(t, do(), http.StatusUnauthorized, "unauthorized")
	// A good token reaches the MCP server.
	rec = do(asBearer(f.full.token))
	wantStatus(t, rec, http.StatusOK, "")
	if !strings.Contains(rec.Body.String(), "list_channels") {
		t.Errorf("tools/list body = %s", rec.Body.String())
	}
}

func TestMCPMissingScopeIsAToolError(t *testing.T) {
	f := newMCPFixture(t)
	for name, cs := range map[string]*mcp.ClientSession{"local": f.local(t, f.readBot.token), "remote": f.remote(t, f.readBot.token)} {
		res := callTool(t, cs, "post_message", map[string]any{"channel": "ops", "text": "hi"})
		if !res.IsError || !strings.Contains(resultText(res), "messages:write") {
			t.Errorf("%s: post_message = %+v", name, res)
		}
		res = callTool(t, cs, "post_alert", f.alertArgs())
		if !res.IsError || !strings.Contains(resultText(res), "alerts:write") {
			t.Errorf("%s: post_alert = %+v", name, res)
		}
		res = callTool(t, cs, "list_alerts", map[string]any{})
		if !res.IsError {
			t.Errorf("%s: list_alerts without alerts:write succeeded", name)
		}
		// The read scopes it has still work.
		structured(t, callTool(t, cs, "list_channels", nil))
	}
	if n := len(f.alice.get("/api/v1/channels/" + f.ops.ID + "/messages").Body.String()); n == 0 {
		t.Fatal("empty channel page")
	}
}

func TestMCPReadMessagesIsLimitedToTheBotsChannels(t *testing.T) {
	f := newMCPFixture(t)
	for name, cs := range map[string]*mcp.ClientSession{"local": f.local(t, f.full.token), "remote": f.remote(t, f.full.token)} {
		out := structured(t, callTool(t, cs, "read_messages", map[string]any{"channel": "ops"}))
		msgs := out["messages"].([]any)
		if len(msgs) == 0 || msgs[len(msgs)-1].(map[string]any)["body"] != "disk report for the cluster" {
			t.Errorf("%s: read_messages = %v", name, out)
		}
		if structured(t, callTool(t, cs, "read_messages", map[string]any{"channel": f.ops.ID}))["channel_id"] != f.ops.ID {
			t.Errorf("%s: channel by id", name)
		}
		// Private channel the bot is not in: by name and by id, an error without data.
		for _, ref := range []string{"secret", "#secret", f.secret.ID} {
			res := callTool(t, cs, "read_messages", map[string]any{"channel": ref})
			if !res.IsError || strings.Contains(resultText(res), "launch codes") || res.StructuredContent != nil {
				t.Errorf("%s: read_messages(%q) = %+v", name, ref, res)
			}
		}
		res := callTool(t, cs, "read_messages", map[string]any{"channel": "ops", "limit": 101})
		if !res.IsError {
			t.Errorf("%s: limit 101 accepted", name)
		}
		hits := structured(t, callTool(t, cs, "search_messages", map[string]any{"query": "launch"}))["results"].([]any)
		if len(hits) != 0 {
			t.Errorf("%s: search leaked a private channel: %v", name, hits)
		}
		hits = structured(t, callTool(t, cs, "search_messages", map[string]any{"query": "disk", "channel": "#ops"}))["results"].([]any)
		if len(hits) != 1 {
			t.Errorf("%s: search = %v", name, hits)
		}
		list := structured(t, callTool(t, cs, "list_channels", nil))["channels"].([]any)
		if len(list) != 1 || list[0].(map[string]any)["name"] != "ops" {
			t.Errorf("%s: list_channels = %v", name, list)
		}
	}
}

func TestMCPPostMessageIsFromTheBot(t *testing.T) {
	f := newMCPFixture(t)
	for name, cs := range map[string]*mcp.ClientSession{"local": f.local(t, f.full.token), "remote": f.remote(t, f.full.token)} {
		body := "hello from " + name
		out := structured(t, callTool(t, cs, "post_message", map[string]any{"channel": "#ops", "text": body}))
		msg := out["message"].(map[string]any)
		if msg["body"] != body || msg["author_kind"] != "app" {
			t.Fatalf("%s: post_message = %v", name, out)
		}
		rec := f.alice.get("/api/v1/channels/" + f.ops.ID + "/messages")
		page := decodeBody[protocol.MessagePage](t, rec)
		last := page.Items[len(page.Items)-1]
		if last.Body != body || last.Author.ID != f.full.app.UserID || last.ID != msg["id"] {
			t.Errorf("%s: stored message = %+v", name, last)
		}
		reply := structured(t, callTool(t, cs, "post_message", map[string]any{"channel": "ops", "text": "re", "thread_root_id": f.hello.ID}))
		if reply["message"].(map[string]any)["thread_root_id"] != f.hello.ID {
			t.Errorf("%s: reply = %v", name, reply)
		}
		// Not a member of #secret: refused, nothing posted.
		res := callTool(t, cs, "post_message", map[string]any{"channel": f.secret.ID, "text": "x"})
		if !res.IsError {
			t.Errorf("%s: posted to a private channel the bot is not in", name)
		}
	}
}

func TestMCPAlertsDedupeAckAndResolve(t *testing.T) {
	f := newMCPFixture(t)
	for name, cs := range map[string]*mcp.ClientSession{"local": f.local(t, f.full.token), "remote": f.remote(t, f.full.token)} {
		args := f.alertArgs()
		args["dedupe_key"] = "disk-" + name
		first := structured(t, callTool(t, cs, "post_alert", args))
		second := structured(t, callTool(t, cs, "post_alert", args))
		m1, m2 := first["message"].(map[string]any), second["message"].(map[string]any)
		if first["transition"] != "created" || m1["id"] != m2["id"] || second["transition"] == "created" {
			t.Fatalf("%s: dedupe failed: %v / %v", name, first, second)
		}
		if a := m2["alert"].(map[string]any); a["occurrences"] != float64(2) || a["state"] != "open" {
			t.Errorf("%s: alert = %v", name, a)
		}
		open := structured(t, callTool(t, cs, "list_alerts", map[string]any{"state": "open", "channel": "ops"}))["alerts"].([]any)
		if len(open) == 0 || open[0].(map[string]any)["id"] != m1["id"] {
			t.Errorf("%s: list_alerts = %v", name, open)
		}
		// Apps cannot ack or resolve by id.
		for tool, in := range map[string]map[string]any{
			"ack_alert":     {"id": m1["id"]},
			"resolve_alert": {"id": m1["id"]},
		} {
			if res := callTool(t, cs, tool, in); !res.IsError {
				t.Errorf("%s: %s by id succeeded", name, tool)
			}
		}
		if res := callTool(t, cs, "resolve_alert", map[string]any{}); !res.IsError {
			t.Errorf("%s: resolve_alert with neither id nor key succeeded", name)
		}
		done := structured(t, callTool(t, cs, "resolve_alert", map[string]any{"dedupe_key": args["dedupe_key"], "text": "freed space"}))
		if a := done["message"].(map[string]any)["alert"].(map[string]any); a["state"] != "resolved" {
			t.Errorf("%s: resolve = %v", name, done)
		}
	}
}

// TestMCPBackendsGiveIdenticalResults runs the same calls against both
// backends over one fixture and compares what the tools return.
func TestMCPBackendsGiveIdenticalResults(t *testing.T) {
	f := newMCPFixture(t)
	local, remote := f.local(t, f.full.token), f.remote(t, f.full.token)
	structured(t, callTool(t, local, "post_alert", f.alertArgs()))
	f.alice.post(t, f.ops.ID, "second line about the disk")

	calls := []struct {
		tool string
		args map[string]any
	}{
		{"list_channels", nil},
		{"read_messages", map[string]any{"channel": "ops"}},
		{"read_messages", map[string]any{"channel": "#ops", "limit": 1}},
		{"read_messages", map[string]any{"channel": "secret"}},
		{"search_messages", map[string]any{"query": "disk"}},
		{"search_messages", map[string]any{"query": "disk", "channel": "ops"}},
		{"list_alerts", map[string]any{}},
		{"list_alerts", map[string]any{"state": "resolved"}},
		{"ack_alert", map[string]any{"id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
		{"resolve_alert", map[string]any{"id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}},
		{"post_message", map[string]any{"channel": "secret", "text": "x"}},
	}
	for _, c := range calls {
		l, r := callTool(t, local, c.tool, c.args), callTool(t, remote, c.tool, c.args)
		if l.IsError != r.IsError {
			t.Errorf("%s %v: IsError local=%v remote=%v (%s | %s)", c.tool, c.args, l.IsError, r.IsError, resultText(l), resultText(r))
			continue
		}
		if l.IsError {
			continue
		}
		lb, _ := json.Marshal(l.StructuredContent)
		rb, _ := json.Marshal(r.StructuredContent)
		if string(lb) != string(rb) {
			t.Errorf("%s %v differs:\nlocal  %s\nremote %s", c.tool, c.args, lb, rb)
		}
		if resultText(l) != resultText(r) {
			t.Errorf("%s %v text differs:\nlocal  %s\nremote %s", c.tool, c.args, resultText(l), resultText(r))
		}
	}
}
