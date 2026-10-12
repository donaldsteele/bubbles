package core

import (
	"context"
	"errors"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Backend is the part of the SDK client the monitor needs (a fake in tests).
type Backend interface {
	Bootstrap(ctx context.Context) (*protocol.Bootstrap, error)
	Events(ctx context.Context) (<-chan protocol.Event, error)
	EventsErr() error
}

// Status is the connection state shown in the tray menu.
type Status string

const (
	StatusConnecting Status = "connecting"
	StatusOnline     Status = "online"
	StatusOffline    Status = "offline"
)

// Sink receives what the monitor produces. Calls come from one goroutine.
type Sink interface {
	Notify(n Notification)
	Indicator(ind Indicator)
	Status(s Status)
}

// Outcome is why Run returned.
type Outcome int

const (
	// OutcomeStopped: the context ended.
	OutcomeStopped Outcome = iota
	// OutcomeSessionEnded: the server answered 401 or closed the socket with
	// 4009 or 4010: the device token is void (revoked or server reset). The
	// caller forgets the token and asks for a new sign-in.
	OutcomeSessionEnded
	// OutcomeIdentityChanged: the server certificate no longer chains to the
	// pinned CA. The caller starts the fingerprint confirmation flow; the
	// token stays untouched and is not sent anywhere.
	OutcomeIdentityChanged
)

// Monitor keeps the background connection: bootstrap, event stream,
// notification decisions, reconnects with backoff.
type Monitor struct {
	Backend Backend
	Engine  *Engine
	Sink    Sink
	// Now and Sleep are injectable for tests; Sleep reports false when ctx
	// ended first.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) bool
}

const (
	minRetry = time.Second
	maxRetry = time.Minute
)

func retryDelay(failures int) time.Duration {
	d := minRetry << min(failures-1, 6)
	if d > maxRetry || d <= 0 {
		return maxRetry
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// classify maps an SDK error to a terminal outcome.
func classify(err error) (Outcome, bool) {
	switch {
	case errors.Is(err, bubblesclient.ErrServerIdentityChanged):
		return OutcomeIdentityChanged, true
	case errors.Is(err, bubblesclient.ErrUnauthorized):
		return OutcomeSessionEnded, true
	}
	return OutcomeStopped, false
}

// Run blocks until the context ends or the session cannot continue.
func (m *Monitor) Run(ctx context.Context) Outcome {
	if m.Now == nil {
		m.Now = time.Now
	}
	if m.Sleep == nil {
		m.Sleep = sleepCtx
	}
	failures := 0
	for {
		if ctx.Err() != nil {
			return OutcomeStopped
		}
		m.Sink.Status(StatusConnecting)
		out, retry := m.session(ctx, &failures)
		if !retry {
			return out
		}
		m.Sink.Status(StatusOffline)
		if !m.Sleep(ctx, retryDelay(failures)) {
			return OutcomeStopped
		}
	}
}

// session runs one bootstrap + stream cycle. retry is true when the cycle
// ended for a recoverable reason.
func (m *Monitor) session(ctx context.Context, failures *int) (out Outcome, retry bool) {
	b, err := m.Backend.Bootstrap(ctx)
	if err != nil {
		return m.failed(ctx, err, failures)
	}
	m.Engine.Load(b)
	evs, err := m.Backend.Events(ctx)
	if err != nil {
		return m.failed(ctx, err, failures)
	}
	*failures = 0
	m.Sink.Status(StatusOnline)
	m.Sink.Indicator(m.Engine.Indicator())
	for ev := range evs {
		res := m.Engine.Handle(ev, m.Now())
		if res.Refresh {
			nb, err := m.Backend.Bootstrap(ctx)
			if err != nil {
				if o, fatal := classify(err); fatal {
					return o, false
				}
			} else {
				m.Engine.Load(nb)
			}
		}
		if res.Notify != nil {
			m.Sink.Notify(*res.Notify)
		}
		if res.Changed || res.Notify != nil {
			m.Sink.Indicator(m.Engine.Indicator())
		}
	}
	if ctx.Err() != nil {
		return OutcomeStopped, false
	}
	return m.failed(ctx, m.Backend.EventsErr(), failures)
}

func (m *Monitor) failed(ctx context.Context, err error, failures *int) (Outcome, bool) {
	if ctx.Err() != nil {
		return OutcomeStopped, false
	}
	if o, fatal := classify(err); fatal {
		return o, false
	}
	*failures++
	return OutcomeStopped, true
}
