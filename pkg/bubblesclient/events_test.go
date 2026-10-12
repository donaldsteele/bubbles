package bubblesclient

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TestEventsBackoffResetsAfterReconnect checks that a successful reconnect
// clears the failure count, so the next disconnect backs off from the minimum
// rather than continuing the exponential sequence.
func TestEventsBackoffResetsAfterReconnect(t *testing.T) {
	pki := newPKI(t)
	var attempts atomic.Int32
	hello, err := json.Marshal(protocol.Hello{Type: protocol.FrameHello, InstanceID: "inst", Epoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 2 || n == 3 {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, hello)
		// Attempts 1 and 4 end with an abnormal close, which counts as a failure.
		_ = conn.Close(websocket.StatusInternalError, "bye")
	}), true)

	c := newClient(t, srv, pki.caFP)
	s := &eventStream{c: c}
	var sleeps []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) bool {
		sleeps = append(sleeps, d)
		return len(sleeps) < 4
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := s.connect(ctx)
	if err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	out := make(chan protocol.Event, 16)
	s.run(ctx, conn, out)

	if len(sleeps) != 4 {
		t.Fatalf("sleeps = %v, want 4 entries", sleeps)
	}
	// Failures 1..3 before the successful reconnect: exponential growth.
	if sleeps[2] <= eventBackoffBase {
		t.Fatalf("third backoff %v should exceed the minimum %v", sleeps[2], eventBackoffBase)
	}
	// After the success the next disconnect must back off from the minimum again.
	if sleeps[3] > eventBackoffBase || sleeps[3] < eventBackoffBase/2 {
		t.Fatalf("backoff after reconnect = %v, want within [%v, %v]", sleeps[3], eventBackoffBase/2, eventBackoffBase)
	}
}
