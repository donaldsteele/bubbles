package bubblesclient

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// DefaultQueueMax is the AlertQueue bound when max <= 0.
const DefaultQueueMax = 1000

// ErrQueueFull is returned when the queue is at capacity and holds nothing
// older than the item in flight.
var ErrQueueFull = errors.New("bubblesclient: alert queue full")

// ErrQueueClosed is returned after Close.
var ErrQueueClosed = errors.New("bubblesclient: alert queue closed")

// ErrMissingDedupeKey is returned for an alert without a DedupeKey. Such
// alerts cannot be coalesced safely, so they are rejected instead of queued.
var ErrMissingDedupeKey = errors.New("bubblesclient: alert has no dedupe key")

const (
	queueBackoffBase = time.Second
	queueBackoffMax  = 60 * time.Second
	// queueRetryAfterMax caps a server-supplied Retry-After so a hostile or
	// misconfigured server cannot stall the queue indefinitely.
	queueRetryAfterMax = 10 * time.Minute
)

// queueItem is the pending state of one dedupe key.
type queueItem struct {
	key     string
	resolve bool
	alert   protocol.AlertInput
	text    string // resolve note
	gen     int    // bumped by every coalesced update
}

// AlertQueue delivers alerts in the background so they survive brief server
// outages. It is bounded, coalesces updates by dedupe key (the newest wins,
// the position of the first-queued entry is kept) and retries 429, 5xx and
// network failures, honouring Retry-After. ErrUnauthorized or
// ErrServerIdentityChanged stop it for good; the cause is available from Err
// and passed to the OnStop callback. Other 4xx answers drop the item
// (counted by Dropped).
type AlertQueue struct {
	c   *Client
	max int

	mu       sync.Mutex
	items    map[string]*queueItem
	order    []string
	inflight string
	started  bool
	closed   bool
	err      error
	dropped  int
	onStop   func(error)
	wake     chan struct{}
	done     chan struct{}
	idle     chan struct{} // closed-and-replaced when the queue drains

	ctx    context.Context
	cancel context.CancelFunc

	// test hooks
	sleep    func(context.Context, time.Duration) bool
	baseWait time.Duration
}

// NewAlertQueue returns a queue sending through c; max <= 0 means 1000.
func NewAlertQueue(c *Client, max int) *AlertQueue {
	if max <= 0 {
		max = DefaultQueueMax
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &AlertQueue{
		c: c, max: max, items: map[string]*queueItem{},
		wake: make(chan struct{}, 1), done: make(chan struct{}), idle: make(chan struct{}),
		ctx: ctx, cancel: cancel, sleep: sleepCtx, baseWait: queueBackoffBase,
	}
}

// SetOnStop registers fn, called once (from the queue goroutine) when the
// queue stops because of ErrUnauthorized or ErrServerIdentityChanged.
func (q *AlertQueue) SetOnStop(fn func(error)) {
	q.mu.Lock()
	q.onStop = fn
	q.mu.Unlock()
}

// Err returns the error that stopped the queue, or nil.
func (q *AlertQueue) Err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

// Done is closed when the queue has stopped (Close or a fatal error).
func (q *AlertQueue) Done() <-chan struct{} { return q.done }

// Len is the number of pending dedupe keys.
func (q *AlertQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Dropped counts items discarded for overflow or a permanent rejection.
func (q *AlertQueue) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

// Enqueue queues an alert post or update.
func (q *AlertQueue) Enqueue(a protocol.AlertInput) error {
	return q.put(&queueItem{key: a.DedupeKey, alert: a})
}

// EnqueueResolve queues a resolve for the alert with dedupe key.
func (q *AlertQueue) EnqueueResolve(dedupeKey, text string) error {
	return q.put(&queueItem{key: dedupeKey, resolve: true, text: text})
}

func (q *AlertQueue) put(it *queueItem) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.err != nil {
		if q.err != nil {
			return q.err
		}
		return ErrQueueClosed
	}
	if it.key == "" {
		return ErrMissingDedupeKey
	}
	if old, ok := q.items[it.key]; ok {
		it.gen = old.gen + 1
		*old = *it
		q.signal()
		return nil
	}
	if len(q.items) >= q.max {
		victim := -1
		for i, k := range q.order {
			if k != q.inflight {
				victim = i
				break
			}
		}
		if victim < 0 {
			return ErrQueueFull
		}
		delete(q.items, q.order[victim])
		q.order = append(q.order[:victim], q.order[victim+1:]...)
		q.dropped++
	}
	q.items[it.key] = it
	q.order = append(q.order, it.key)
	if !q.started {
		q.started = true
		go q.run()
	}
	q.signal()
	return nil
}

func (q *AlertQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Flush blocks until the queue is empty, ctx ends, or the queue stops.
func (q *AlertQueue) Flush(ctx context.Context) error {
	for {
		q.mu.Lock()
		empty, err, idle := len(q.items) == 0, q.err, q.idle
		q.mu.Unlock()
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		select {
		case <-idle:
		case <-q.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the queue; pending items are discarded.
func (q *AlertQueue) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	started := q.started
	q.mu.Unlock()
	q.cancel()
	if started {
		<-q.done
	} else {
		close(q.done)
	}
}

func (q *AlertQueue) head() (queueItem, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		q.inflight = ""
		return queueItem{}, false
	}
	k := q.order[0]
	q.inflight = k
	return *q.items[k], true
}

// finish removes the sent item unless it was updated meanwhile.
func (q *AlertQueue) finish(it queueItem, remove bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inflight = ""
	cur, ok := q.items[it.key]
	if !ok || !remove || cur.gen != it.gen {
		return
	}
	delete(q.items, it.key)
	for i, k := range q.order {
		if k == it.key {
			q.order = append(q.order[:i], q.order[i+1:]...)
			break
		}
	}
	if len(q.items) == 0 {
		close(q.idle)
		q.idle = make(chan struct{})
	}
}

func (q *AlertQueue) stop(err error) {
	q.mu.Lock()
	if err != nil && q.err == nil {
		q.err = err
	}
	cb := q.onStop
	q.mu.Unlock()
	if err != nil && cb != nil {
		cb(err)
	}
}

func (q *AlertQueue) run() {
	defer close(q.done)
	failures := 0
	for {
		it, ok := q.head()
		if !ok {
			select {
			case <-q.wake:
				continue
			case <-q.ctx.Done():
				return
			}
		}
		err := q.send(it)
		switch {
		case err == nil:
			q.finish(it, true)
			failures = 0
		case q.ctx.Err() != nil:
			return
		case fatal(err):
			q.stop(err)
			return
		case retryable(err):
			q.finish(it, false)
			failures++
			wait := q.baseWait << min(failures-1, 10)
			if wait > queueBackoffMax || wait <= 0 {
				wait = queueBackoffMax
			}
			var ae *APIError
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				wait = min(ae.RetryAfter, queueRetryAfterMax)
			}
			if !q.sleep(q.ctx, wait) {
				return
			}
		default:
			var ae *APIError
			if errors.As(err, &ae) && it.resolve && ae.Status == 404 {
				q.finish(it, true) // nothing to resolve
				continue
			}
			q.finish(it, true)
			q.mu.Lock()
			q.dropped++
			q.mu.Unlock()
		}
	}
}

func (q *AlertQueue) send(it queueItem) error {
	ctx, cancel := context.WithTimeout(q.ctx, 30*time.Second)
	defer cancel()
	if it.resolve {
		return q.c.ResolveAlert(ctx, it.key, it.text)
	}
	_, err := q.c.PostAlert(ctx, it.alert)
	return err
}
