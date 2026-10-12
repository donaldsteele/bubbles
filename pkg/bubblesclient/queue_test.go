package bubblesclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type queueServer struct {
	mu     sync.Mutex
	status func(n int, in protocol.AlertInput) (int, string)
	n      int
	posted []string // "post:<key>:<title>" or "resolve:<key>"
}

func (s *queueServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.n++
	n := s.n
	var in protocol.AlertInput
	var res protocol.ResolveAlertByKeyRequest
	rec := ""
	if r.URL.Path == "/api/v1/alerts/resolve" {
		_ = json.NewDecoder(r.Body).Decode(&res)
		in.DedupeKey = res.DedupeKey
		rec = "resolve:" + res.DedupeKey
	} else {
		_ = json.NewDecoder(r.Body).Decode(&in)
		rec = "post:" + in.DedupeKey + ":" + in.Title
	}
	status, retry := s.status(n, in)
	if status < 300 {
		s.posted = append(s.posted, rec)
	}
	s.mu.Unlock()
	if retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorBody{Error: protocol.Error{Code: "x", Message: "m"}})
}

func (s *queueServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func queueFor(t *testing.T, st func(int, protocol.AlertInput) (int, string)) (*AlertQueue, *queueServer) {
	t.Helper()
	pki := newPKI(t)
	qs := &queueServer{status: st}
	srv := pki.server(t, qs, true)
	q := NewAlertQueue(newClient(t, srv, pki.caFP), 0)
	t.Cleanup(q.Close)
	return q, qs
}

func TestQueueCoalescesWhile503(t *testing.T) {
	q, qs := queueFor(t, func(int, protocol.AlertInput) (int, string) { return 503, "" })
	for i := range 50 {
		if err := q.Enqueue(protocol.AlertInput{DedupeKey: "k", Title: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(400 * time.Millisecond)
	if n := qs.count(); n > 2 {
		t.Fatalf("%d requests for 50 coalesced updates", n)
	}
	if q.Len() != 1 {
		t.Fatalf("len = %d", q.Len())
	}
}

func TestQueueNewestWinsAndOrder(t *testing.T) {
	q, qs := queueFor(t, func(int, protocol.AlertInput) (int, string) { return 200, "" })
	// Pause the worker by holding the lock-free path: enqueue before the
	// worker can run is racy, so fill under a blocked server instead.
	qs.mu.Lock()
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "a", Title: "a1"})
	time.Sleep(50 * time.Millisecond) // worker is now blocked on qs.mu with "a" in flight
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "b", Title: "b1"})
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "c", Title: "c1"})
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "b", Title: "b2"})
	qs.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	qs.mu.Lock()
	defer qs.mu.Unlock()
	want := []string{"post:a:a1", "post:b:b2", "post:c:c1"}
	if len(qs.posted) != len(want) {
		t.Fatalf("posted %v", qs.posted)
	}
	for i := range want {
		if qs.posted[i] != want[i] {
			t.Fatalf("posted %v want %v", qs.posted, want)
		}
	}
}

func TestQueueRetryAfterHonoured(t *testing.T) {
	var waits []time.Duration
	var mu sync.Mutex
	q, qs := queueFor(t, func(n int, _ protocol.AlertInput) (int, string) {
		if n == 1 {
			return 429, "9"
		}
		return 201, ""
	})
	q.sleep = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return true
	}
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "k", Title: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 1 || waits[0] != 9*time.Second || qs.count() != 2 {
		t.Fatalf("waits=%v requests=%d", waits, qs.count())
	}
}

func TestQueueStopsOnUnauthorized(t *testing.T) {
	q, qs := queueFor(t, func(int, protocol.AlertInput) (int, string) { return 401, "" })
	var cb atomic.Value
	q.SetOnStop(func(err error) { cb.Store(err) })
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "k"})
	select {
	case <-q.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("queue did not stop")
	}
	if !errors.Is(q.Err(), ErrUnauthorized) {
		t.Fatalf("Err = %v", q.Err())
	}
	if e, _ := cb.Load().(error); !errors.Is(e, ErrUnauthorized) {
		t.Fatalf("callback err = %v", e)
	}
	if err := q.Enqueue(protocol.AlertInput{DedupeKey: "k2"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Enqueue after stop = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if qs.count() != 1 {
		t.Fatalf("requests = %d, want 1", qs.count())
	}
	if err := q.Flush(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Flush = %v", err)
	}
}

func TestQueueDropsPermanentRejectionAndResolve404(t *testing.T) {
	q, qs := queueFor(t, func(n int, in protocol.AlertInput) (int, string) {
		if in.Title == "bad" {
			return 400, ""
		}
		if in.DedupeKey == "gone" {
			return 404, ""
		}
		return 200, ""
	})
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "x", Title: "bad"})
	_ = q.EnqueueResolve("gone", "")
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "ok", Title: "fine"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	qs.mu.Lock()
	defer qs.mu.Unlock()
	if len(qs.posted) != 1 || qs.posted[0] != "post:ok:fine" || q.Dropped() != 1 {
		t.Fatalf("posted=%v dropped=%d", qs.posted, q.Dropped())
	}
}

func TestQueueBounded(t *testing.T) {
	pki := newPKI(t)
	block := make(chan struct{})
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }), true)
	defer close(block)
	q := NewAlertQueue(newClient(t, srv, pki.caFP), 3)
	defer q.Close()
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if err := q.Enqueue(protocol.AlertInput{DedupeKey: k}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if q.Len() != 3 || q.Dropped() != 2 {
		t.Fatalf("len=%d dropped=%d", q.Len(), q.Dropped())
	}
}

func TestQueueRetryAfterClamped(t *testing.T) {
	var waits []time.Duration
	var mu sync.Mutex
	q, qs := queueFor(t, func(n int, _ protocol.AlertInput) (int, string) {
		if n == 1 {
			return 429, "86400"
		}
		return 201, ""
	})
	q.sleep = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return true
	}
	_ = q.Enqueue(protocol.AlertInput{DedupeKey: "k", Title: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := q.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 1 || waits[0] != queueRetryAfterMax || qs.count() != 2 {
		t.Fatalf("waits=%v requests=%d", waits, qs.count())
	}
}

func TestQueueRejectsMissingDedupeKey(t *testing.T) {
	q, qs := queueFor(t, func(int, protocol.AlertInput) (int, string) { return 201, "" })
	if err := q.Enqueue(protocol.AlertInput{Title: "no key"}); !errors.Is(err, ErrMissingDedupeKey) {
		t.Fatalf("Enqueue err = %v", err)
	}
	if err := q.EnqueueResolve("", "note"); !errors.Is(err, ErrMissingDedupeKey) {
		t.Fatalf("EnqueueResolve err = %v", err)
	}
	if q.Len() != 0 || qs.count() != 0 {
		t.Fatalf("len=%d requests=%d", q.Len(), qs.count())
	}
}
