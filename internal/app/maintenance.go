package app

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/donaldsteele/bubbles/internal/httpapi"
)

// MaintenanceRetryAfter is the Retry-After (seconds) of a maintenance 503.
const MaintenanceRetryAfter = "5"

// maintenance is the gate in front of both listeners. While a reset runs,
// every request except GET /healthz is answered 503 resetting (WebSocket
// upgrades included). It also counts the requests in flight so a reset can
// wait for them to finish before it deletes anything.
type maintenance struct {
	on     atomic.Bool
	active atomic.Int64
}

// gateKey marks a request context that the gate counts as in flight.
type gateKey struct{}

func (m *maintenance) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		// Counted before the check: once drain has seen the count settle
		// with the gate closed, no request can slip through any more.
		m.active.Add(1)
		defer m.active.Add(-1)
		if m.on.Load() {
			w.Header().Set("Retry-After", MaintenanceRetryAfter)
			httpapi.WriteError(w, http.StatusServiceUnavailable, httpapi.CodeUnavailable, "resetting")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), gateKey{}, true)))
	})
}

func (m *maintenance) enter()       { m.on.Store(true) }
func (m *maintenance) leave()       { m.on.Store(false) }
func (m *maintenance) closed() bool { return m.on.Load() }

// self is how many in-flight requests belong to the caller: 1 when ctx is
// a request that came through the gate (the reset request itself).
func (m *maintenance) self(ctx context.Context) int64 {
	if ctx.Value(gateKey{}) != nil {
		return 1
	}
	return 0
}

// drain waits until at most self requests are in flight, or timeout. It
// reports whether the server went quiet.
func (m *maintenance) drain(self int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for m.active.Load() > self {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// handlerSlot is an http.Handler whose target can be replaced atomically
// (the services behind it are rebuilt by a factory reset).
type handlerSlot struct {
	p atomic.Pointer[handlerRef]
}

type handlerRef struct{ h http.Handler }

func (s *handlerSlot) set(h http.Handler) { s.p.Store(&handlerRef{h}) }

func (s *handlerSlot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.p.Load().h.ServeHTTP(w, r)
}
