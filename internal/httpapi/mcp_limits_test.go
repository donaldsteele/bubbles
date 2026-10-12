package httpapi

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// frozenBearer adds the bearer token without stepping the fake clock (unlike
// bearerTripper), so the post limits only move with the test's own requests.
type frozenBearer struct {
	token string
	base  http.RoundTripper
}

func (b frozenBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// frozenHTTP is an HTTPS client for the fixture's server that keeps the clock
// still.
func frozenHTTP(f *mcpFixture, token string) *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: f.pki.pool}}
	return &http.Client{Transport: frozenBearer{token: token, base: tr}}
}

// frozenLocal connects an MCP client to the endpoint without stepping the clock.
func frozenLocal(t *testing.T, f *mcpFixture, token string) *mcp.ClientSession {
	t.Helper()
	cl := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := cl.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: f.srv.URL + "/api/v1/mcp", HTTPClient: frozenHTTP(f, token),
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// isRateLimited reports whether a tool result is the limiter's refusal.
func isRateLimited(res *mcp.CallToolResult) bool {
	return res.IsError && strings.Contains(resultText(res), "rate limited, retry after ")
}

// TestMCPPostAlertIsRateLimitedPerApp bursts post_alert over MCP past the
// per-app cap: the burst goes through and the rest are tool errors.
func TestMCPPostAlertIsRateLimitedPerApp(t *testing.T) {
	f := newMCPFixture(t)
	cs := frozenLocal(t, f, f.full.token)
	var limited int
	for i := 0; i < alertBurst+1; i++ {
		args := f.alertArgs()
		args["dedupe_key"] = fmt.Sprintf("burst-%d", i)
		res := callTool(t, cs, "post_alert", args)
		switch {
		case isRateLimited(res):
			limited++
		case res.IsError:
			t.Fatalf("post_alert %d: %s", i, resultText(res))
		}
	}
	if limited != 1 {
		t.Fatalf("rate-limited calls = %d, want 1 (burst %d)", limited, alertBurst)
	}
}

// TestRESTPostsExhaustTheMCPPostLimit posts through REST until the per-user
// burst is spent, then checks that post_message over MCP, which the bot makes
// as the same user, is refused by the same bucket.
func TestRESTPostsExhaustTheMCPPostLimit(t *testing.T) {
	f := newMCPFixture(t)
	c := frozenHTTP(f, f.full.token)
	url := f.srv.URL + "/api/v1/channels/" + f.ops.ID + "/messages"
	post := func() int {
		t.Helper()
		resp, err := c.Post(url, "application/json", strings.NewReader(`{"body":"rest"}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < messagePostBurst; i++ {
		if code := post(); code != http.StatusCreated {
			t.Fatalf("REST post %d: status %d, want 201", i, code)
		}
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("REST post past the burst: status %d, want 429", code)
	}

	cs := frozenLocal(t, f, f.full.token)
	res := callTool(t, cs, "post_message", map[string]any{"channel": "#ops", "text": "via mcp"})
	if !isRateLimited(res) {
		t.Fatalf("post_message after the REST burst was not rate limited: %v", resultText(res))
	}
}
