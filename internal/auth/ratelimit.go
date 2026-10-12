package auth

import (
	"math"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Rate limits (plan: REST API, rate limits).
const (
	LoginUserMaxFailures = 5
	LoginIPMaxFailures   = 20
	LoginWindow          = 15 * time.Minute
	APIRatePerSecond     = 20
	APIBurst             = 50

	// SignupIPMaxAttempts is how many sign-up attempts (register and
	// device-register) one IP address may make within SignupWindow.
	SignupIPMaxAttempts = 20
	SignupWindow        = time.Hour

	// limiterMaxKeys caps the memory of every limiter: when exceeded,
	// expired entries are dropped, then the oldest ones.
	limiterMaxKeys = 10000
)

// FailureLimiter blocks a key after max failures within a sliding window.
// It is in memory and safe for concurrent use.
type FailureLimiter struct {
	// mu is a pointer so the two limiters of a LoginLimiter share one lock:
	// a login reservation checks and records both keys atomically.
	mu      *sync.Mutex
	now     func() time.Time
	max     int
	window  time.Duration
	maxKeys int
	hits    map[string][]time.Time
}

// NewFailureLimiter returns a limiter that blocks a key after max failures
// within window. now is the clock (nil means time.Now).
func NewFailureLimiter(now func() time.Time, max int, window time.Duration) *FailureLimiter {
	if now == nil {
		now = time.Now
	}
	return &FailureLimiter{mu: new(sync.Mutex), now: now, max: max, window: window, maxKeys: limiterMaxKeys, hits: map[string][]time.Time{}}
}

// prune drops hits older than the window; callers hold l.mu.
func (l *FailureLimiter) prune(key string, now time.Time) []time.Time {
	hits := l.hits[key]
	i := 0
	for i < len(hits) && !hits[i].After(now.Add(-l.window)) {
		i++
	}
	if i > 0 {
		hits = hits[i:]
		if len(hits) == 0 {
			delete(l.hits, key)
		} else {
			l.hits[key] = hits
		}
	}
	return hits
}

// Blocked reports whether key has used up its failures and, if so, how long
// until the oldest failure leaves the window.
func (l *FailureLimiter) Blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blockedLocked(key, l.now())
}

// blockedLocked is Blocked for a caller that holds l.mu.
func (l *FailureLimiter) blockedLocked(key string, now time.Time) (time.Duration, bool) {
	hits := l.prune(key, now)
	if len(hits) < l.max {
		return 0, false
	}
	return hits[len(hits)-l.max].Add(l.window).Sub(now), true
}

// Fail records one failure for key.
func (l *FailureLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.addLocked(key, l.now())
}

// addLocked records one failure for key at now. Callers hold l.mu.
func (l *FailureLimiter) addLocked(key string, now time.Time) {
	l.prune(key, now)
	if _, known := l.hits[key]; !known && len(l.hits) >= l.maxKeys {
		l.evictLocked(now)
	}
	hits := append(l.hits[key], now)
	if len(hits) > l.max {
		hits = hits[len(hits)-l.max:]
	}
	l.hits[key] = hits
}

// refundLocked removes the failure of key recorded at at, if it is still in
// the window. Callers hold l.mu.
func (l *FailureLimiter) refundLocked(key string, at time.Time) {
	hits := l.hits[key]
	for i := len(hits) - 1; i >= 0; i-- {
		if hits[i].Equal(at) {
			hits = append(hits[:i], hits[i+1:]...)
			break
		}
	}
	if len(hits) == 0 {
		delete(l.hits, key)
		return
	}
	l.hits[key] = hits
}

// evictLocked makes room for a new key once maxKeys is reached. Keys with no
// failure left in the window go first. Then the least recently failed key
// that is not locked out goes. A locked key is dropped only when every key is
// locked, so an active lockout is never dropped while an unlocked key exists.
// Callers hold l.mu.
func (l *FailureLimiter) evictLocked(now time.Time) {
	var unlocked, locked string
	var unlockedLast, lockedLast time.Time
	for k := range l.hits {
		hits := l.prune(k, now)
		if len(hits) == 0 {
			continue
		}
		last := hits[len(hits)-1]
		if len(hits) >= l.max {
			if locked == "" || last.Before(lockedLast) {
				locked, lockedLast = k, last
			}
		} else if unlocked == "" || last.Before(unlockedLast) {
			unlocked, unlockedLast = k, last
		}
	}
	if len(l.hits) < l.maxKeys {
		return
	}
	victim := unlocked
	if victim == "" {
		victim = locked
	}
	if victim != "" {
		delete(l.hits, victim)
	}
}

// Reset forgets the failures of key.
func (l *FailureLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// LoginLimiter combines the per-username and per-IP failure limits.
type LoginLimiter struct {
	users *FailureLimiter
	ips   *FailureLimiter
}

// NewLoginLimiter returns the production limits: 5 failures per username
// and 20 per IP address within 15 minutes.
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	users := NewFailureLimiter(now, LoginUserMaxFailures, LoginWindow)
	ips := NewFailureLimiter(now, LoginIPMaxFailures, LoginWindow)
	ips.mu = users.mu
	return &LoginLimiter{users: users, ips: ips}
}

// IPKey returns the key an IP limiter uses for a client address. IPv4 (and
// IPv4-mapped IPv6) addresses are keyed in full. An IPv6 address is keyed by
// its /64 prefix, since one host usually owns a whole /64 and could otherwise
// rotate addresses to dodge a per-address limit. Input that does not parse is
// keyed as given; an empty address has no key.
func IPKey(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	prefix, _ := addr.Prefix(64)
	return prefix.String()
}

// Check reports whether a login attempt for username from ip is blocked and
// how long the caller must wait. It reserves nothing: code that verifies a
// password must use Begin, or a burst of concurrent attempts can all pass.
func (l *LoginLimiter) Check(username, ip string) (time.Duration, bool) {
	l.users.mu.Lock()
	defer l.users.mu.Unlock()
	return l.blockedLocked(username, IPKey(ip), l.users.now())
}

// blockedLocked is Check for a caller that holds the shared lock. An empty
// key is not limited.
func (l *LoginLimiter) blockedLocked(username, ip string, now time.Time) (time.Duration, bool) {
	var ru, ri time.Duration
	var bu, bi bool
	if username != "" {
		ru, bu = l.users.blockedLocked(username, now)
	}
	if ip != "" {
		ri, bi = l.ips.blockedLocked(ip, now)
	}
	switch {
	case bu && bi:
		return max(ru, ri), true
	case bu:
		return ru, true
	case bi:
		return ri, true
	}
	return 0, false
}

// Fail records a failed attempt against username and ip. Prefer Begin when a
// password is being checked: Fail does not stop a burst of attempts that
// are already in flight.
func (l *LoginLimiter) Fail(username, ip string) {
	l.users.mu.Lock()
	defer l.users.mu.Unlock()
	now := l.users.now()
	if username != "" {
		l.users.addLocked(username, now)
	}
	if ipk := IPKey(ip); ipk != "" {
		l.ips.addLocked(ipk, now)
	}
}

// Succeed clears the failures of username (the IP count is kept).
func (l *LoginLimiter) Succeed(username string) { l.users.Reset(username) }

// LoginAttempt is an attempt reserved by LoginLimiter.Begin before its
// password is checked. The reservation counts as a failure from the start.
// Succeed settles it as a success, Abort undoes it, and leaving it alone keeps
// the failure.
type LoginAttempt struct {
	l    *LoginLimiter
	key  string // "" when the key is not limited
	ip   string // "" when the IP is not limited
	at   time.Time
	done bool
}

// Begin reserves one attempt for key (a normalized username, or another
// per-account key) from ip, before any password is checked. The limit check
// and the reservation happen under one lock, so concurrent attempts cannot all
// pass the check before one of them is recorded. When either key is at its
// limit Begin reserves nothing and returns the wait.
func (l *LoginLimiter) Begin(key, ip string) (*LoginAttempt, time.Duration, bool) {
	ipk := IPKey(ip)
	l.users.mu.Lock()
	defer l.users.mu.Unlock()
	now := l.users.now()
	if wait, blocked := l.blockedLocked(key, ipk, now); blocked {
		return nil, wait, true
	}
	if key != "" {
		l.users.addLocked(key, now)
	}
	if ipk != "" {
		l.ips.addLocked(ipk, now)
	}
	return &LoginAttempt{l: l, key: key, ip: ipk, at: now}, 0, false
}

// Succeed settles the attempt as a success: the key's failures are cleared
// and the IP slot is refunded. It does nothing after Succeed or Abort.
func (a *LoginAttempt) Succeed() {
	a.l.users.mu.Lock()
	defer a.l.users.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if a.key != "" {
		delete(a.l.users.hits, a.key)
	}
	if a.ip != "" {
		a.l.ips.refundLocked(a.ip, a.at)
	}
}

// Abort settles the attempt as if it never happened: both slots are refunded.
// Use it when no credential check ran (cancelled request, lookup error) or
// when the credentials were right but the request was refused for another
// reason. It does nothing after Succeed or Abort.
func (a *LoginAttempt) Abort() {
	a.l.users.mu.Lock()
	defer a.l.users.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if a.key != "" {
		a.l.users.refundLocked(a.key, a.at)
	}
	if a.ip != "" {
		a.l.ips.refundLocked(a.ip, a.at)
	}
}

// BucketLimiter is a token bucket per key: rate tokens per second up to
// burst. It is in memory and safe for concurrent use.
type BucketLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	rate    float64
	burst   float64
	buckets map[string]*bucket
	sweep   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewBucketLimiter returns a limiter that refills ratePerSecond tokens per
// second per key, up to burst. now is the clock (nil means time.Now).
func NewBucketLimiter(now func() time.Time, ratePerSecond float64, burst int) *BucketLimiter {
	if now == nil {
		now = time.Now
	}
	return &BucketLimiter{now: now, rate: ratePerSecond, burst: float64(burst), buckets: map[string]*bucket{}}
}

// NewAPILimiter returns the per-session API limit: 20 requests per second,
// burst 50.
func NewAPILimiter(now func() time.Time) *BucketLimiter {
	return NewBucketLimiter(now, APIRatePerSecond, APIBurst)
}

// Allow takes one token from key's bucket. When none is left it returns
// false and the time until one is available.
func (b *BucketLimiter) Allow(key string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.sweepLocked(now)
	bk := b.buckets[key]
	if bk == nil {
		bk = &bucket{tokens: b.burst, last: now}
		b.buckets[key] = bk
	}
	if el := now.Sub(bk.last).Seconds(); el > 0 {
		bk.tokens = math.Min(b.burst, bk.tokens+el*b.rate)
		bk.last = now
	}
	if bk.tokens >= 1 {
		bk.tokens--
		return true, 0
	}
	return false, time.Duration((1 - bk.tokens) / b.rate * float64(time.Second))
}

// sweepLocked drops buckets that have refilled completely (they behave like
// new ones) once a minute, or immediately when there are too many.
func (b *BucketLimiter) sweepLocked(now time.Time) {
	if len(b.buckets) < limiterMaxKeys && now.Before(b.sweep) {
		return
	}
	b.sweep = now.Add(time.Minute)
	for k, bk := range b.buckets {
		if bk.tokens+now.Sub(bk.last).Seconds()*b.rate >= b.burst {
			delete(b.buckets, k)
		}
	}
}

// RetryAfterSeconds rounds d up to whole seconds for a Retry-After header
// (at least 1).
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	return max(s, 1)
}
