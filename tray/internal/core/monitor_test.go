package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
	"github.com/donaldsteele/bubbles/pkg/notifyrules"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// step is one scripted connection cycle of the fake backend: Bootstrap
// starts the cycle, Events continues it and EventsErr ends it.
type step struct {
	bootstrapErr error
	eventsErr    error
	events       []protocol.Event
	endErr       error // EventsErr after the stream closed
}

type scriptedBackend struct {
	mu    sync.Mutex
	steps []step
	idx   int
	cur   step
}

func (s *scriptedBackend) Bootstrap(context.Context) (*protocol.Bootstrap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx < len(s.steps) {
		s.cur = s.steps[s.idx]
		s.idx++
	} else {
		s.cur = step{endErr: errors.New("script exhausted")}
	}
	if s.cur.bootstrapErr != nil {
		return nil, s.cur.bootstrapErr
	}
	return &protocol.Bootstrap{
		Me:          protocol.Me{User: protocol.User{ID: "me", DisplayName: "Me"}},
		Users:       []protocol.User{{ID: "bob", DisplayName: "Bob"}},
		Channels:    []protocol.Channel{{ID: "c1", Kind: notifyrules.ChannelPublic, Name: str("general")}},
		Memberships: []protocol.Membership{{ChannelID: "c1", UserID: "me", NotifyLevel: "all", AlertMinSeverity: "info"}},
	}, nil
}

func (s *scriptedBackend) Events(context.Context) (<-chan protocol.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur.eventsErr != nil {
		return nil, s.cur.eventsErr
	}
	ch := make(chan protocol.Event, len(s.cur.events)+1)
	for _, e := range s.cur.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (s *scriptedBackend) EventsErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.endErr
}

type recSink struct {
	notes    []Notification
	statuses []Status
	inds     []Indicator
}

func (r *recSink) Notify(n Notification) { r.notes = append(r.notes, n) }
func (r *recSink) Indicator(i Indicator) { r.inds = append(r.inds, i) }
func (r *recSink) Status(s Status)       { r.statuses = append(r.statuses, s) }
func (r *recSink) sawStatus(s Status) bool {
	for _, x := range r.statuses {
		if x == s {
			return true
		}
	}
	return false
}

func msgEvent(t *testing.T, id string) protocol.Event {
	t.Helper()
	m := protocol.Message{ID: id, ChannelID: "c1", Kind: notifyrules.KindText, Body: "hi " + id, Author: protocol.Author{ID: "bob"}}
	return protocol.Event{Seq: 1, Type: "message.created", ChannelID: "c1", Data: raw(t, m)}
}

func runMonitor(t *testing.T, steps []step, sleeps *[]time.Duration) (Outcome, *recSink) {
	t.Helper()
	be := &scriptedBackend{steps: steps}
	sink := &recSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Monitor{
		Backend: be, Engine: NewEngine(), Sink: sink, Now: time.Now,
		Sleep: func(_ context.Context, d time.Duration) bool {
			*sleeps = append(*sleeps, d)
			return len(*sleeps) < 20
		},
	}
	done := make(chan Outcome, 1)
	go func() { done <- m.Run(ctx) }()
	select {
	case o := <-done:
		return o, sink
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not stop")
		return 0, nil
	}
}

func TestMonitorTerminalOutcomes(t *testing.T) {
	apiErr := func(status int) error { return &bubblesclient.APIError{Status: status, Code: "x", Message: "x"} }
	tests := []struct {
		name  string
		steps []step
		want  Outcome
	}{
		{"bootstrap 401 (token revoked or server reset)", []step{{bootstrapErr: apiErr(401)}}, OutcomeSessionEnded},
		{"stream closed with 4009/4010", []step{{endErr: bubblesclient.ErrUnauthorized}}, OutcomeSessionEnded},
		{"events refused with 401", []step{{eventsErr: apiErr(401)}}, OutcomeSessionEnded},
		{"pinned CA mismatch at bootstrap", []step{{bootstrapErr: fmt.Errorf("x: %w", bubblesclient.ErrServerIdentityChanged)}}, OutcomeIdentityChanged},
		{"pinned CA mismatch on reconnect", []step{{endErr: bubblesclient.ErrUnavailable}, {eventsErr: bubblesclient.ErrServerIdentityChanged}}, OutcomeIdentityChanged},
		{"stream ends with identity change", []step{{endErr: bubblesclient.ErrServerIdentityChanged}}, OutcomeIdentityChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sleeps []time.Duration
			got, _ := runMonitor(t, tc.steps, &sleeps)
			if got != tc.want {
				t.Fatalf("outcome = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMonitorNotifiesAndReconnectsWithBackoff(t *testing.T) {
	var sleeps []time.Duration
	steps := []step{
		{bootstrapErr: bubblesclient.ErrUnavailable},
		{bootstrapErr: bubblesclient.ErrUnavailable},
		{events: nil, endErr: bubblesclient.ErrUnavailable},
		{endErr: bubblesclient.ErrUnauthorized},
	}
	got, sink := runMonitor(t, steps, &sleeps)
	if got != OutcomeSessionEnded {
		t.Fatalf("outcome = %v", got)
	}
	if len(sleeps) < 3 || sleeps[0] != time.Second || sleeps[1] != 2*time.Second || sleeps[2] != time.Second {
		t.Fatalf("backoff = %v", sleeps)
	}
	if !sink.sawStatus(StatusOffline) || !sink.sawStatus(StatusOnline) || !sink.sawStatus(StatusConnecting) {
		t.Fatalf("statuses = %v", sink.statuses)
	}
}

func TestMonitorDeliversNotificationsAndIndicators(t *testing.T) {
	var sleeps []time.Duration
	steps := []step{{events: []protocol.Event{msgEvent(t, "m1"), msgEvent(t, "m2")}, endErr: bubblesclient.ErrUnauthorized}}
	got, sink := runMonitor(t, steps, &sleeps)
	if got != OutcomeSessionEnded {
		t.Fatalf("outcome = %v", got)
	}
	if len(sink.notes) != 2 || sink.notes[0].Title != "Bob in #general" || sink.notes[1].MessageID != "m2" {
		t.Fatalf("notes = %+v", sink.notes)
	}
	if len(sink.inds) == 0 || sink.inds[len(sink.inds)-1] != IndicatorUnread {
		t.Fatalf("indicators = %v", sink.inds)
	}
}

func TestMonitorStopsWithContext(t *testing.T) {
	be := &scriptedBackend{steps: []step{{endErr: bubblesclient.ErrUnavailable}}}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Monitor{
		Backend: be, Engine: NewEngine(), Sink: &recSink{},
		Sleep: func(ctx context.Context, d time.Duration) bool { cancel(); return false },
	}
	if got := m.Run(ctx); got != OutcomeStopped {
		t.Fatalf("outcome = %v", got)
	}
}

func TestRetryDelay(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second, time.Minute, time.Minute}
	for i, w := range want {
		if got := retryDelay(i + 1); got != w {
			t.Errorf("retryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
	if retryDelay(1000) != time.Minute {
		t.Error("delay is not capped")
	}
}
