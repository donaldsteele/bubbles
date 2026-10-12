package mcpserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// manualClock is a fake clock that only moves when Advance is called.
type manualClock struct{ t time.Time }

func (c *manualClock) Now() time.Time          { return c.t }
func (c *manualClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestRateLimitedErrorText(t *testing.T) {
	cases := []struct {
		wait time.Duration
		want string
	}{
		{0, "rate limited, retry after 1s"},
		{time.Second, "rate limited, retry after 1s"},
		{1500 * time.Millisecond, "rate limited, retry after 2s"},
		{42 * time.Second, "rate limited, retry after 42s"},
	}
	for _, tc := range cases {
		if got := (RateLimitedError{RetryAfter: tc.wait}).Error(); got != tc.want {
			t.Errorf("wait %v: got %q, want %q", tc.wait, got, tc.want)
		}
	}
}

// TestLocalPostsShareTheRESTLimits checks that PostMessage and PostAlert draw
// from the same limiters the REST handlers use, keyed by the bot's user id,
// and refuse with a RateLimitedError once the burst is spent.
func TestLocalPostsShareTheRESTLimits(t *testing.T) {
	clock := &manualClock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	lim := Limits{
		Posts:  auth.NewBucketLimiter(clock.Now, 1, 2),
		Alerts: auth.NewBucketLimiter(clock.Now, 1, 2),
	}
	actor := chat.Actor{ID: "bot"}
	// Spend the bot's burst on the shared limiters, as REST posts would.
	for i := 0; i < 2; i++ {
		if ok, _ := lim.Posts.Allow(actor.ID); !ok {
			t.Fatalf("post %d refused before the burst ran out", i)
		}
		if ok, _ := lim.Alerts.Allow(actor.ID); !ok {
			t.Fatalf("alert %d refused before the burst ran out", i)
		}
	}
	// Services is empty: a refused post must return before touching them.
	b := NewLocal(Services{}, actor, audit.Actor{}, lim)
	ctx := context.Background()

	_, err := b.PostMessage(ctx, "channel", protocol.NewMessage{Body: "hi"})
	var rl RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("PostMessage error = %v, want RateLimitedError", err)
	}
	_, err = b.PostAlert(ctx, protocol.AlertInput{DedupeKey: "disk"})
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("PostAlert error = %v, want RateLimitedError", err)
	}

	// The bucket refills with the clock: one second restores one token.
	clock.Advance(time.Second)
	if ok, _ := lim.Posts.Allow(actor.ID); !ok {
		t.Error("post limiter did not refill after one second")
	}
}
