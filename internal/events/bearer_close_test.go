package events

import (
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// subscribeBearer registers a bearer connection of an app token.
func (w *world) subscribeBearer(user, tokenID, appID string, expiresAt int64) *testSub {
	ts := &testSub{closed: make(chan closeCall, 1)}
	v := Viewer{UserID: user, Role: protocol.RoleMember, Kind: protocol.KindApp,
		TokenID: tokenID, AppID: appID, TokenExpiresAt: expiresAt}
	ts.Subscription = w.hub.Subscribe(v, "", func(code int, reason string) { ts.closed <- closeCall{code, reason} })
	return ts
}

// expectBearerClosed waits up to 1s for s to be closed with 4009.
func expectBearerClosed(t *testing.T, s *testSub, name string) {
	t.Helper()
	select {
	case c := <-s.closed:
		if c.code != CloseSessionRevoked {
			t.Fatalf("%s closed with %d, want 4009", name, c.code)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s not closed within 1s", name)
	}
}

// expectBearerOpen reports an error if s was closed.
func expectBearerOpen(t *testing.T, s *testSub, name string) {
	t.Helper()
	select {
	case c := <-s.closed:
		t.Fatalf("%s closed unexpectedly (%d)", name, c.code)
	default:
	}
}

func TestRevokedTokenClosesOnlyItsBearerConnections(t *testing.T) {
	w := newWorld(t)
	revoked := w.subscribeBearer("bot", "t1", "a1", 0)
	sibling := w.subscribeBearer("bot", "t2", "a1", 0)
	otherApp := w.subscribeBearer("bot2", "t3", "a2", 0)
	cookie := w.subscribe("bot", protocol.RoleMember, "s9")

	w.commit(t, nil, ev(TypeSessionRevoked, "", "bot", `{"token_id":"t1","app_id":"a1"}`))
	expectBearerClosed(t, revoked, "revoked token")

	m := w.marker(t)
	until(t, sibling, m)
	until(t, otherApp, m)
	until(t, cookie, m)
	expectBearerOpen(t, sibling, "sibling token of the same app")
	expectBearerOpen(t, otherApp, "token of another app")
	expectBearerOpen(t, cookie, "cookie session of the bot user")
}

func TestAppDisableClosesAllItsBearerConnections(t *testing.T) {
	w := newWorld(t)
	t1 := w.subscribeBearer("bot", "t1", "a1", 0)
	t2 := w.subscribeBearer("bot", "t2", "a1", 0)
	other := w.subscribeBearer("bot2", "t3", "a2", 0)

	w.commit(t, nil, ev(TypeSessionRevoked, "", "bot", `{"app_id":"a1","app_disabled":true}`))
	expectBearerClosed(t, t1, "token 1")
	expectBearerClosed(t, t2, "token 2")

	until(t, other, w.marker(t))
	expectBearerOpen(t, other, "other app")
}

func TestBearerConnectionClosesAtTokenExpiry(t *testing.T) {
	w := newWorld(t)
	expiresAt := w.clock.Now().Add(50 * time.Millisecond).UnixMilli()
	expiring := w.subscribeBearer("bot", "t1", "a1", expiresAt)
	forever := w.subscribeBearer("bot", "t2", "a1", 0)

	expectBearerClosed(t, expiring, "expiring token")
	until(t, forever, w.marker(t))
	expectBearerOpen(t, forever, "non-expiring token")
}

func TestAlreadyExpiredBearerConnectionClosesAtOnce(t *testing.T) {
	w := newWorld(t)
	expired := w.subscribeBearer("bot", "t1", "a1", w.clock.Now().Add(-time.Second).UnixMilli())
	expectBearerClosed(t, expired, "expired token")
}

func TestDetachedBearerConnectionIsNotClosedByExpiry(t *testing.T) {
	w := newWorld(t)
	expiresAt := w.clock.Now().Add(50 * time.Millisecond).UnixMilli()
	s := w.subscribeBearer("bot", "t1", "a1", expiresAt)
	s.Leave()
	time.Sleep(100 * time.Millisecond)
	expectBearerOpen(t, s, "left subscription")
}
