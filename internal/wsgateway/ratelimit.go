package wsgateway

import "time"

// Client frame rate limit, per connection: a token bucket that refills at
// clientFrameRate tokens per second up to clientFrameBurst. A client frame that
// finds no token closes the socket with 1008.
const (
	clientFrameRate  = 10.0
	clientFrameBurst = 20.0
)

// tokenBucket is a token bucket owned by one reader goroutine.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newTokenBucket returns a full bucket whose refill clock starts at now.
func newTokenBucket(now time.Time) *tokenBucket {
	return &tokenBucket{tokens: clientFrameBurst, last: now}
}

// take spends one token at now and reports whether one was available.
func (b *tokenBucket) take(now time.Time) bool {
	if now.After(b.last) {
		b.tokens = min(clientFrameBurst, b.tokens+now.Sub(b.last).Seconds()*clientFrameRate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
