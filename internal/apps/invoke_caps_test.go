package apps

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// reserveN reserves n invocations for (app, invoker) pairs made by make(i) and
// returns the first error.
func reserveN(t *testing.T, s *Service, now int64, n int, make func(i int) (string, string)) error {
	t.Helper()
	for i := 0; i < n; i++ {
		appID, invoker := make(i)
		s.invMu.Lock()
		err := s.reserveLocked(now, fmt.Sprintf("inv-%d-%s-%s", i, appID, invoker),
			&invocation{appID: appID, invokerID: invoker, expires: now + InvocationTTL.Milliseconds()})
		s.invMu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func TestPendingInvocationCaps(t *testing.T) {
	tests := []struct {
		name string
		// reserve makes the reservations; the last one is the probe.
		reserve func(i int) (string, string)
		total   int
		wantErr bool
	}{
		{"per app up to the cap", func(i int) (string, string) { return "a1", fmt.Sprintf("u%d", i) }, MaxPendingPerApp, false},
		{"per app refused past the cap", func(i int) (string, string) { return "a1", fmt.Sprintf("u%d", i) }, MaxPendingPerApp + 1, true},
		{"per invoker up to the cap", func(i int) (string, string) { return fmt.Sprintf("a%d", i), "u1" }, MaxPendingPerInvoker, false},
		{"per invoker refused past the cap", func(i int) (string, string) { return fmt.Sprintf("a%d", i), "u1" }, MaxPendingPerInvoker + 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
			s := New(Options{Now: clock.Now})
			err := reserveN(t, s, clock.Now().UnixMilli(), tt.total, tt.reserve)
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("error = %v, want refusal %v", err, tt.wantErr)
			}
			if err != nil {
				var pending *PendingLimitError
				if !errors.As(err, &pending) {
					t.Fatalf("error %T, want *PendingLimitError", err)
				}
				if pending.RetryAfter < time.Second {
					t.Fatalf("RetryAfter = %v, want at least 1s", pending.RetryAfter)
				}
			}
		})
	}
}

func TestPendingCapFreesAfterExpiry(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Now: clock.Now})
	fill := func(i int) (string, string) { return "a1", fmt.Sprintf("u%d", i) }
	if err := reserveN(t, s, clock.Now().UnixMilli(), MaxPendingPerApp, fill); err != nil {
		t.Fatal(err)
	}
	clock.Advance(InvocationTTL + time.Second)
	if err := reserveN(t, s, clock.Now().UnixMilli(), 1, func(int) (string, string) { return "a1", "late" }); err != nil {
		t.Fatalf("reservation after expiry refused: %v", err)
	}
	// One budgeted sweep popped sweepBudget of the expired entries; the new one is counted.
	if got, want := s.invByApp["a1"], MaxPendingPerApp-sweepBudget+1; got != want {
		t.Fatalf("pending for a1 = %d, want %d", got, want)
	}
}

func TestRetryAfterIsTimeUntilOldestExpires(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Now: clock.Now})
	fill := func(i int) (string, string) { return "a1", fmt.Sprintf("u%d", i) }
	if err := reserveN(t, s, clock.Now().UnixMilli(), MaxPendingPerApp, fill); err != nil {
		t.Fatal(err)
	}
	clock.Advance(4 * time.Minute)
	err := reserveN(t, s, clock.Now().UnixMilli(), 1, func(int) (string, string) { return "a1", "late" })
	var pending *PendingLimitError
	if !errors.As(err, &pending) {
		t.Fatalf("error = %v, want *PendingLimitError", err)
	}
	if want := InvocationTTL - 4*time.Minute; pending.RetryAfter != want {
		t.Fatalf("RetryAfter = %v, want %v", pending.RetryAfter, want)
	}
}

func TestExpirySweepIsBudgeted(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	s := New(Options{Now: clock.Now})
	// Spread over many apps and invokers so no cap is reached.
	const n = 200
	spread := func(i int) (string, string) { return fmt.Sprintf("a%d", i), fmt.Sprintf("u%d", i) }
	if err := reserveN(t, s, clock.Now().UnixMilli(), n, spread); err != nil {
		t.Fatal(err)
	}
	clock.Advance(InvocationTTL + time.Second)
	// One reservation pops at most sweepBudget expired entries.
	if err := reserveN(t, s, clock.Now().UnixMilli(), 1, func(int) (string, string) { return "z", "z" }); err != nil {
		t.Fatal(err)
	}
	if got, want := len(s.invQueue), n-sweepBudget+1; got != want {
		t.Fatalf("queue length = %d, want %d after one budgeted sweep", got, want)
	}
	if got, want := len(s.invocations), n-sweepBudget+1; got != want {
		t.Fatalf("invocations = %d, want %d", got, want)
	}
	if err := reserveN(t, s, clock.Now().UnixMilli(), 1, func(int) (string, string) { return "z", "y" }); err != nil {
		t.Fatal(err)
	}
	sum := func(m map[string]int) (n int) {
		for _, v := range m {
			n += v
		}
		return n
	}
	if sum(s.invByApp) != len(s.invQueue) || sum(s.invByInvoker) != len(s.invQueue) {
		t.Fatalf("pending counts %v %v do not match queue length %d", s.invByApp, s.invByInvoker, len(s.invQueue))
	}
}
