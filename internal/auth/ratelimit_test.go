package auth

import (
	"fmt"
	"testing"
	"time"
)

func TestFailureLimiter(t *testing.T) {
	clock := newFakeClock()
	l := NewFailureLimiter(clock.Now, 5, 15*time.Minute)
	for i := 0; i < 4; i++ {
		l.Fail("alice")
		clock.Advance(time.Minute)
	}
	if _, blocked := l.Blocked("alice"); blocked {
		t.Fatal("blocked after 4 failures")
	}
	l.Fail("alice") // 5th failure, at t=4m; the first was at t=0
	wait, blocked := l.Blocked("alice")
	if !blocked || wait != 11*time.Minute {
		t.Fatalf("Blocked = %v, %v; want true, 11m (first failure leaves the window at 15m)", wait, blocked)
	}
	if _, blocked := l.Blocked("bob"); blocked {
		t.Error("bob blocked by alice's failures")
	}
	clock.Advance(11*time.Minute - time.Second)
	if _, blocked := l.Blocked("alice"); !blocked {
		t.Error("unblocked a second early")
	}
	clock.Advance(time.Second)
	if _, blocked := l.Blocked("alice"); blocked {
		t.Error("still blocked after the window")
	}
	for i := 0; i < 5; i++ {
		l.Fail("alice")
	}
	l.Reset("alice")
	if _, blocked := l.Blocked("alice"); blocked {
		t.Error("Reset did not clear")
	}
}

func TestFailureLimiterBoundedMemory(t *testing.T) {
	clock := newFakeClock()
	l := NewFailureLimiter(clock.Now, 5, 15*time.Minute)
	for i := 0; i < limiterMaxKeys+500; i++ {
		l.Fail(fmt.Sprintf("user%d", i))
		clock.Advance(time.Millisecond)
	}
	if n := len(l.hits); n > limiterMaxKeys {
		t.Errorf("%d keys retained, cap %d", n, limiterMaxKeys)
	}
}

func TestLoginLimiter(t *testing.T) {
	clock := newFakeClock()
	l := NewLoginLimiter(clock.Now)
	for i := 0; i < LoginUserMaxFailures; i++ {
		l.Fail("alice", "10.0.0.1")
	}
	if _, blocked := l.Check("alice", "10.0.0.2"); !blocked {
		t.Error("username not blocked after 5 failures")
	}
	if _, blocked := l.Check("bob", "10.0.0.2"); blocked {
		t.Error("unrelated username/IP blocked")
	}
	l.Succeed("alice")
	if _, blocked := l.Check("alice", "10.0.0.2"); blocked {
		t.Error("success did not clear the username")
	}
	// 20 failures from one IP across different usernames block the IP.
	for i := 0; i < LoginIPMaxFailures; i++ {
		l.Fail(fmt.Sprintf("u%d", i), "10.0.0.9")
	}
	if wait, blocked := l.Check("fresh", "10.0.0.9"); !blocked || wait <= 0 || wait > LoginWindow {
		t.Errorf("IP Check = %v, %v", wait, blocked)
	}
	if _, blocked := l.Check("fresh", "10.0.0.10"); blocked {
		t.Error("other IP blocked")
	}
}

func TestBucketLimiter(t *testing.T) {
	clock := newFakeClock()
	b := NewAPILimiter(clock.Now)
	for i := 0; i < APIBurst; i++ {
		if ok, _ := b.Allow("s1"); !ok {
			t.Fatalf("request %d denied within burst", i+1)
		}
	}
	ok, wait := b.Allow("s1")
	if ok || wait != 50*time.Millisecond {
		t.Fatalf("over burst: ok=%v wait=%v, want denied, 50ms", ok, wait)
	}
	if ok, _ := b.Allow("s2"); !ok {
		t.Error("another session shares the bucket")
	}
	clock.Advance(100 * time.Millisecond) // 2 tokens at 20/s
	if ok, _ := b.Allow("s1"); !ok {
		t.Error("no refill after 100ms")
	}
	if ok, _ := b.Allow("s1"); !ok {
		t.Error("second refilled token missing")
	}
	if ok, _ := b.Allow("s1"); ok {
		t.Error("third request allowed")
	}
	clock.Advance(time.Hour)
	b.Allow("s3") // triggers a sweep of fully refilled buckets
	if n := len(b.buckets); n > 2 {
		t.Errorf("%d buckets retained after sweep", n)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	for d, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1, 1500 * time.Millisecond: 2, 11 * time.Minute: 660} {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}
