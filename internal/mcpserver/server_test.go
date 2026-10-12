package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// fake is a Backend over fixed data that records the calls it receives.
type fake struct {
	scopes   []string
	channels []protocol.Channel
	readID   string
	readN    int
	query    string
	cursor   string
	next     *string
}

func (f *fake) Scopes(context.Context) ([]string, error)                 { return f.scopes, nil }
func (f *fake) ListChannels(context.Context) ([]protocol.Channel, error) { return f.channels, nil }
func (f *fake) ReadMessages(_ context.Context, id, _ string, n int) (protocol.MessagePage, error) {
	f.readID, f.readN = id, n
	return protocol.MessagePage{Items: []protocol.Message{{ID: "m1", ChannelID: id, Body: "hi"}}}, nil
}
func (f *fake) Search(_ context.Context, q, cursor string) (protocol.SearchPage, error) {
	f.query, f.cursor = q, cursor
	return protocol.SearchPage{NextCursor: f.next}, nil
}
func (f *fake) PostMessage(context.Context, string, protocol.NewMessage) (protocol.Message, error) {
	return protocol.Message{ID: "m2"}, nil
}
func (f *fake) ListAlerts(_ context.Context, _, _, cursor string) (protocol.AlertPage, error) {
	f.cursor = cursor
	return protocol.AlertPage{NextCursor: f.next}, nil
}
func (f *fake) PostAlert(context.Context, protocol.AlertInput) (protocol.PostedAlert, error) {
	return protocol.PostedAlert{}, nil
}
func (f *fake) AckAlert(context.Context, string) (protocol.Message, error) {
	return protocol.Message{}, errors.New("apps cannot acknowledge alerts")
}
func (f *fake) ResolveAlert(context.Context, string, string, string) (protocol.Message, error) {
	return protocol.Message{ID: "a1"}, nil
}

const opsID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func newFake(scopes ...string) *fake {
	name := "ops"
	return &fake{scopes: scopes, channels: []protocol.Channel{{ID: opsID, Kind: "public", Name: &name}}}
}

func connect(t *testing.T, b Backend) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := New(b, Info{}).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func text0(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestMCPToolList(t *testing.T) {
	cs := connect(t, newFake())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
	}
	want := slices.Clone(ToolNames)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || len(got) != 8 {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestMCPScopeRules(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		tool   string
		args   map[string]any
		want   string // "" = allowed, else a substring of the error
	}{
		{"no mcp", []string{"messages:read"}, "list_channels", nil, "mcp scope"},
		{"read ok", []string{"mcp", "messages:read"}, "list_channels", nil, ""},
		{"read needs messages:read", []string{"mcp", "alerts:write"}, "read_messages", map[string]any{"channel": "ops"}, "messages:read"},
		{"search needs messages:read", []string{"mcp"}, "search_messages", map[string]any{"query": "x"}, "messages:read"},
		{"post needs messages:write", []string{"mcp", "messages:read"}, "post_message", map[string]any{"channel": "ops", "text": "x"}, "messages:write"},
		{"post ok", []string{"mcp", "messages:write"}, "post_message", map[string]any{"channel": "ops", "text": "x"}, ""},
		{"list_alerts needs messages:read", []string{"mcp", "alerts:write"}, "list_alerts", nil, "messages:read"},
		{"list_alerts needs alerts:write", []string{"mcp", "messages:read"}, "list_alerts", nil, "alerts:write"},
		{"post_alert needs alerts:write", []string{"mcp", "messages:write"}, "post_alert", map[string]any{"channel": "ops", "severity": "info", "title": "t", "dedupe_key": "k"}, "alerts:write"},
		{"ack needs alerts:write", []string{"mcp"}, "ack_alert", map[string]any{"id": "x"}, "alerts:write"},
		{"resolve needs alerts:write", []string{"mcp"}, "resolve_alert", map[string]any{"dedupe_key": "k"}, "alerts:write"},
		{"resolve by key ok", []string{"mcp", "alerts:write"}, "resolve_alert", map[string]any{"dedupe_key": "k"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, connect(t, newFake(tc.scopes...)), tc.tool, tc.args)
			if tc.want == "" {
				if res.IsError {
					t.Fatalf("error: %s", text0(res))
				}
				return
			}
			if !res.IsError || !strings.Contains(text0(res), tc.want) {
				t.Fatalf("result = error %v %q, want error containing %q", res.IsError, text0(res), tc.want)
			}
		})
	}
}

func TestMCPChannelReferencesAndLimits(t *testing.T) {
	f := newFake("mcp", "messages:read")
	cs := connect(t, f)
	for _, ref := range []string{"ops", "#ops", "OPS", opsID} {
		f.readID = ""
		if res := call(t, cs, "read_messages", map[string]any{"channel": ref}); res.IsError || f.readID != opsID || f.readN != 50 {
			t.Errorf("channel %q: error=%v id=%q n=%d (%s)", ref, res.IsError, f.readID, f.readN, text0(res))
		}
	}
	if res := call(t, cs, "read_messages", map[string]any{"channel": "nope"}); !res.IsError {
		t.Error("unknown channel accepted")
	}
	if res := call(t, cs, "read_messages", map[string]any{"channel": "ops", "limit": 100}); res.IsError || f.readN != 100 {
		t.Errorf("limit 100: %v %d", res.IsError, f.readN)
	}
	for _, n := range []int{101, -1} {
		if res := call(t, cs, "read_messages", map[string]any{"channel": "ops", "limit": n}); !res.IsError {
			t.Errorf("limit %d accepted", n)
		}
	}
	call(t, cs, "search_messages", map[string]any{"query": "disk", "channel": "#ops"})
	if f.query != "disk in:#ops" {
		t.Errorf("search query = %q", f.query)
	}
}

func TestMCPAckByIDIsAToolError(t *testing.T) {
	cs := connect(t, newFake("mcp", "alerts:write"))
	res := call(t, cs, "ack_alert", map[string]any{"id": "m"})
	if !res.IsError || !strings.Contains(text0(res), "cannot acknowledge") {
		t.Fatalf("ack = %+v", res)
	}
	if res := call(t, cs, "resolve_alert", map[string]any{"id": "m", "dedupe_key": "k"}); !res.IsError {
		t.Error("resolve with both id and dedupe_key accepted")
	}
}

func TestMCPCursorsPassThrough(t *testing.T) {
	f := newFake("mcp", "messages:read", "alerts:write")
	next := "opaque-next"
	f.next = &next
	cs := connect(t, f)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"search_messages", map[string]any{"query": "disk", "cursor": " c1 "}},
		{"list_alerts", map[string]any{"cursor": "c1"}},
	} {
		f.cursor = ""
		res := call(t, cs, tc.tool, tc.args)
		if res.IsError {
			t.Fatalf("%s: %s", tc.tool, text0(res))
		}
		if f.cursor != "c1" {
			t.Errorf("%s: backend cursor = %q, want c1", tc.tool, f.cursor)
		}
		sc, _ := res.StructuredContent.(map[string]any)
		if sc["next_cursor"] != next {
			t.Errorf("%s: next_cursor = %v", tc.tool, sc["next_cursor"])
		}
	}
	f.next = nil
	res := call(t, cs, "list_alerts", nil)
	if sc, _ := res.StructuredContent.(map[string]any); sc["next_cursor"] != nil {
		t.Errorf("last page next_cursor = %v, want absent", sc["next_cursor"])
	}
}
