package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countPasswordChecks wraps the service's password verifier and returns a
// counter of the attempts that reached it.
func (e *testEnv) countPasswordChecks() *atomic.Int32 {
	var n atomic.Int32
	inner := e.svc.verify
	e.svc.verify = func(ctx context.Context, password, encoded string) (bool, error) {
		n.Add(1)
		return inner(ctx, password, encoded)
	}
	return &n
}

// burst runs fn n times at once and returns the errors in call order.
func burst(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// tally sorts login results into rate limited, invalid credentials and other.
func tally(errs []error) (limited, invalid, other int) {
	for _, err := range errs {
		var rl *RateLimitedError
		switch {
		case errors.As(err, &rl):
			limited++
		case errors.Is(err, ErrInvalidCredentials):
			invalid++
		default:
			other++
		}
	}
	return limited, invalid, other
}

func TestLoginBurstReachesPasswordCheckAtMostLimit(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	checks := e.countPasswordChecks()
	const attempts = 60
	errs := burst(attempts, func(int) error {
		_, err := e.login("alice", "wrong password!")
		return err
	})
	limited, invalid, other := tally(errs)
	if got := int(checks.Load()); got != LoginUserMaxFailures {
		t.Errorf("%d attempts reached the password check, want %d", got, LoginUserMaxFailures)
	}
	if invalid != LoginUserMaxFailures || limited != attempts-LoginUserMaxFailures || other != 0 {
		t.Errorf("invalid=%d limited=%d other=%d; want %d, %d, 0", invalid, limited, other, LoginUserMaxFailures, attempts-LoginUserMaxFailures)
	}
	for _, err := range errs {
		var rl *RateLimitedError
		if errors.As(err, &rl) && (rl.RetryAfter <= 0 || rl.RetryAfter > LoginWindow) {
			t.Errorf("RetryAfter = %v, want within (0, %v]", rl.RetryAfter, LoginWindow)
			break
		}
	}
	// The right password is refused too while the username is locked.
	var rl *RateLimitedError
	if _, err := e.login("alice", goodPassword); !errors.As(err, &rl) {
		t.Errorf("correct password while locked: %v", err)
	}
}

func TestLoginBurstFromOneIPIsBounded(t *testing.T) {
	e := newEnv(t)
	checks := e.countPasswordChecks()
	const attempts = 60
	errs := burst(attempts, func(i int) error {
		_, err := e.svc.Login(e.ctx, LoginInput{Username: fmt.Sprintf("user%d", i), Password: "wrong password!", IP: "192.168.1.77"})
		return err
	})
	limited, invalid, other := tally(errs)
	if got := int(checks.Load()); got != LoginIPMaxFailures {
		t.Errorf("%d attempts reached the password check, want %d", got, LoginIPMaxFailures)
	}
	if invalid != LoginIPMaxFailures || limited != attempts-LoginIPMaxFailures || other != 0 {
		t.Errorf("invalid=%d limited=%d other=%d", invalid, limited, other)
	}
}

func TestChangePasswordBurstIsBounded(t *testing.T) {
	e := newEnv(t)
	alice := e.addUser("alice", RoleMember)
	checks := e.countPasswordChecks()
	in := ChangePasswordInput{UserID: alice.ID, Current: "wrong password!", New: "a brand new passphrase", IP: "192.168.1.10"}
	const attempts = 60
	errs := burst(attempts, func(int) error { return e.svc.ChangePassword(e.ctx, in) })
	wrong, limited := 0, 0
	for _, err := range errs {
		var rl *RateLimitedError
		switch {
		case errors.Is(err, ErrWrongPassword):
			wrong++
		case errors.As(err, &rl):
			limited++
		}
	}
	if got := int(checks.Load()); got != LoginUserMaxFailures {
		t.Errorf("%d attempts reached the password check, want %d", got, LoginUserMaxFailures)
	}
	if wrong != LoginUserMaxFailures || limited != attempts-LoginUserMaxFailures {
		t.Errorf("wrong=%d limited=%d", wrong, limited)
	}
}

func TestLoginSuccessClearsUsernameFailures(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	for i := 0; i < 3; i++ {
		if _, err := e.login("alice", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	e.mustLogin("alice")
	checks := e.countPasswordChecks()
	for i := 0; i < LoginUserMaxFailures; i++ {
		if _, err := e.login("alice", "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d after the reset reached no check: %v", i+1, err)
		}
	}
	var rl *RateLimitedError
	if _, err := e.login("alice", goodPassword); !errors.As(err, &rl) {
		t.Errorf("6th failure after the reset: %v", err)
	}
	if got := checks.Load(); got != LoginUserMaxFailures {
		t.Errorf("%d checks after the reset, want %d", got, LoginUserMaxFailures)
	}
}

func TestLoginUsernameKeyIsNormalized(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	variants := []string{"ALICE", "Alice", " aLiCe "}
	for i := 0; i < LoginUserMaxFailures; i++ {
		if _, err := e.login(variants[i%len(variants)], "wrong password!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	for _, name := range append(variants, "alice") {
		var rl *RateLimitedError
		if _, err := e.login(name, goodPassword); !errors.As(err, &rl) {
			t.Errorf("%q shares no lockout with alice: %v", name, err)
		}
	}
}

func TestLoginSuccessRefundsIPSlot(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleMember)
	const ip = "192.168.1.78"
	for i := 0; i < LoginIPMaxFailures-1; i++ {
		_, err := e.svc.Login(e.ctx, LoginInput{Username: fmt.Sprintf("guess%d", i), Password: "wrong password!", IP: ip})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d: %v", i+1, err)
		}
	}
	if _, err := e.svc.Login(e.ctx, LoginInput{Username: "alice", Password: goodPassword, IP: ip}); err != nil {
		t.Fatalf("good login: %v", err)
	}
	// The success gave its IP slot back, so one more failure is still checked...
	if _, err := e.svc.Login(e.ctx, LoginInput{Username: "last", Password: "wrong password!", IP: ip}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("20th failure from the IP: %v", err)
	}
	// ...and the attempt after it is refused.
	var rl *RateLimitedError
	if _, err := e.svc.Login(e.ctx, LoginInput{Username: "next", Password: "wrong password!", IP: ip}); !errors.As(err, &rl) {
		t.Errorf("21st attempt from the IP: %v", err)
	}
}

func TestLoginIPv6LimitIsPerPrefix(t *testing.T) {
	e := newEnv(t)
	samePrefix := []string{"2001:db8:1:2::1", "2001:db8:1:2:aaaa::2", "2001:db8:1:2:ffff:ffff:ffff:3"}
	for i := 0; i < LoginIPMaxFailures; i++ {
		_, err := e.svc.Login(e.ctx, LoginInput{Username: fmt.Sprintf("u%d", i), Password: "wrong password!", IP: samePrefix[i%len(samePrefix)]})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	var rl *RateLimitedError
	if _, err := e.svc.Login(e.ctx, LoginInput{Username: "fresh", Password: goodPassword, IP: "2001:db8:1:2:1234::9"}); !errors.As(err, &rl) {
		t.Errorf("another address in the same /64: %v", err)
	}
	for _, ip := range []string{"2001:db8:1:3::1", "192.168.1.90"} {
		if _, err := e.svc.Login(e.ctx, LoginInput{Username: "fresh", Password: "wrong password!", IP: ip}); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s has its own budget but got %v", ip, err)
		}
	}
}

func TestIPKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"192.168.1.10", "192.168.1.10"},
		{"192.168.1.11", "192.168.1.11"},
		{"::ffff:192.168.1.10", "192.168.1.10"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff:ffff:ffff:ffff", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"", ""},
		{"not-an-ip", "not-an-ip"},
	}
	for _, tt := range tests {
		if got := IPKey(tt.in); got != tt.want {
			t.Errorf("IPKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoginAttemptSettlement(t *testing.T) {
	clock := newFakeClock()
	l := NewLoginLimiter(clock.Now)
	count := func(key, ip string) (int, int) {
		l.users.mu.Lock()
		defer l.users.mu.Unlock()
		return len(l.users.hits[key]), len(l.ips.hits[IPKey(ip)])
	}

	a, _, blocked := l.Begin("alice", "10.0.0.1")
	if blocked {
		t.Fatal("first attempt blocked")
	}
	if u, i := count("alice", "10.0.0.1"); u != 1 || i != 1 {
		t.Fatalf("reserved slots = %d, %d; want 1, 1", u, i)
	}
	a.Abort()
	if u, i := count("alice", "10.0.0.1"); u != 0 || i != 0 {
		t.Errorf("after Abort slots = %d, %d; want 0, 0", u, i)
	}

	b, _, _ := l.Begin("alice", "10.0.0.1")
	b.Succeed()
	b.Abort() // no-op after Succeed
	if u, i := count("alice", "10.0.0.1"); u != 0 || i != 0 {
		t.Errorf("after Succeed slots = %d, %d; want 0, 0", u, i)
	}

	// A failed attempt (never settled) keeps both slots.
	for i := 0; i < LoginUserMaxFailures; i++ {
		if _, _, blocked := l.Begin("bob", "10.0.0.2"); blocked {
			t.Fatalf("attempt %d blocked", i+1)
		}
	}
	if _, _, blocked := l.Begin("bob", "10.0.0.2"); !blocked {
		t.Error("attempt past the username limit not blocked")
	}
	if u, _ := count("bob", "10.0.0.2"); u != LoginUserMaxFailures {
		t.Errorf("bob slots = %d, want %d", u, LoginUserMaxFailures)
	}
}

func TestFailureLimiterEvictsUnlockedKeysFirst(t *testing.T) {
	clock := newFakeClock()
	l := NewFailureLimiter(clock.Now, 2, time.Hour)
	l.maxKeys = 3
	l.Fail("locked")
	l.Fail("locked") // locked out, and the least recently failed key overall
	clock.Advance(time.Second)
	l.Fail("old")
	clock.Advance(time.Second)
	l.Fail("new")
	clock.Advance(time.Second)
	l.Fail("fourth") // the map is full: an unlocked key must go

	if _, blocked := l.Blocked("locked"); !blocked {
		t.Fatal("lockout dropped while unlocked keys existed")
	}
	if _, kept := l.hits["old"]; kept {
		t.Error("least recently failed unlocked key was kept")
	}
	if _, kept := l.hits["new"]; !kept {
		t.Error("more recent unlocked key was evicted")
	}
	if len(l.hits) > l.maxKeys {
		t.Errorf("%d keys retained, cap %d", len(l.hits), l.maxKeys)
	}
}

func TestFailureLimiterCapHoldsWhenEveryKeyIsLocked(t *testing.T) {
	clock := newFakeClock()
	l := NewFailureLimiter(clock.Now, 1, time.Hour)
	l.maxKeys = 2
	l.Fail("a")
	clock.Advance(time.Second)
	l.Fail("b")
	clock.Advance(time.Second)
	l.Fail("c")
	if len(l.hits) > l.maxKeys {
		t.Errorf("%d keys retained, cap %d", len(l.hits), l.maxKeys)
	}
	if _, blocked := l.Blocked("c"); !blocked {
		t.Error("newest key not recorded")
	}
}
