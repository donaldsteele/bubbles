package bubblesclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestReadMethodsRequests(t *testing.T) {
	pki := newPKI(t)
	var got []string
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"id":"m1","message":{"id":"m1"},"transition":"created"}`))
	}), true)
	c := newClient(t, srv, pki.caFP)
	ctx := context.Background()

	if _, err := c.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListMessages(ctx, "C1", "M9", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListMessages(ctx, "C1", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, "disk in:#ops", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListAlerts(ctx, "open", "C1", "", 0); err != nil {
		t.Fatal(err)
	}
	if res, err := c.PostAlertResult(ctx, protocol.AlertInput{DedupeKey: "k"}); err != nil || res.Transition != "created" {
		t.Fatalf("PostAlertResult = %+v, %v", res, err)
	}
	if _, err := c.AckAlert(ctx, "M1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveAlertByID(ctx, "M1", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ResolveAlertKey(ctx, "k", ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v1/channels",
		"GET /api/v1/channels/C1/messages?before=M9&limit=20",
		"GET /api/v1/channels/C1/messages",
		"GET /api/v1/search?q=disk+in%3A%23ops",
		"GET /api/v1/alerts?channel_id=C1&state=open",
		"POST /api/v1/alerts",
		"POST /api/v1/alerts/M1/ack",
		"POST /api/v1/alerts/M1/resolve",
		"POST /api/v1/alerts/resolve",
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}
