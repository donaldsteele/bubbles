package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
)

// TestRemoteScopesAreCachedForOneTTL serves the token's scopes from a test
// server and moves a fake clock: the cached scopes hold for 60 s, then the
// backend asks the server again and sees the new scopes.
func TestRemoteScopesAreCachedForOneTTL(t *testing.T) {
	var (
		mu     sync.Mutex
		scopes = []string{"messages:read"}
		hits   atomic.Int32
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		s := scopes
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": map[string]any{"scopes": s}})
	}))
	t.Cleanup(srv.Close)

	c, err := bubblesclient.New(bubblesclient.Config{BaseURL: srv.URL, Token: "tok", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	clock := &manualClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	r := NewRemote(c).(*remote)
	r.now = clock.Now

	steps := []struct {
		name    string
		advance time.Duration
		change  []string // scopes the server reports from this step on
		want    []string
		hits    int32
	}{
		{name: "first fetch", want: []string{"messages:read"}, hits: 1},
		{name: "within the TTL", advance: 59 * time.Second, want: []string{"messages:read"}, hits: 1},
		{name: "after the TTL", advance: 2 * time.Second, change: []string{"messages:read", "alerts:write"},
			want: []string{"messages:read", "alerts:write"}, hits: 2},
		{name: "cached again", want: []string{"messages:read", "alerts:write"}, hits: 2},
	}
	ctx := context.Background()
	for _, st := range steps {
		clock.Advance(st.advance)
		if st.change != nil {
			mu.Lock()
			scopes = st.change
			mu.Unlock()
		}
		got, err := r.Scopes(ctx)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if !reflect.DeepEqual(got, st.want) {
			t.Errorf("%s: scopes = %v, want %v", st.name, got, st.want)
		}
		if n := hits.Load(); n != st.hits {
			t.Errorf("%s: server hits = %d, want %d", st.name, n, st.hits)
		}
	}
}
