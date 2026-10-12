package wsgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestTokenBucketBurstThenRefill(t *testing.T) {
	t0 := time.Unix(1000, 0)
	b := newTokenBucket(t0)
	for i := range int(clientFrameBurst) {
		if !b.take(t0) {
			t.Fatalf("token %d refused inside the burst", i)
		}
	}
	if b.take(t0) {
		t.Fatal("frame beyond the burst allowed in the same instant")
	}
	// 200 ms at 10 tokens/s refills two tokens.
	at := t0.Add(200 * time.Millisecond)
	if !b.take(at) || !b.take(at) || b.take(at) {
		t.Fatal("200 ms did not refill exactly two tokens")
	}
	// A long idle period refills to the burst and no further.
	late := t0.Add(time.Hour)
	for i := range int(clientFrameBurst) {
		if !b.take(late) {
			t.Fatalf("token %d refused after a long idle period", i)
		}
	}
	if b.take(late) {
		t.Fatal("refill exceeded the burst")
	}
}

func TestClientFrameRateLimitCloses1008(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	ping := []byte(`{"type":"ping"}`)
	for range 200 {
		if err := c.ws.Write(context.Background(), websocket.MessageText, ping); err != nil {
			break
		}
	}
	if code := c.closeCode(); code != websocket.StatusPolicyViolation {
		t.Fatalf("flood closed with %d, want 1008", code)
	}
}

func TestClientFramesWithinRateAreNotLimited(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	for range 8 { // about 8 frames a second, below the 10/s refill
		c.send(protocol.ClientFrame{Type: protocol.ClientPing})
		time.Sleep(120 * time.Millisecond)
	}
	c.send(protocol.ClientFrame{Type: protocol.ClientPing})
	for {
		f := c.mustNext()
		if f.Type == protocol.FramePong {
			return
		}
	}
}

func TestCloseIsPromptWithNonReadingClient(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	// The client never reads again. Large settings events back up the socket
	// so the writer blocks in ws.Write.
	payload := json.RawMessage(`{"keys":["` + strings.Repeat("x", 64<<10) + `"]}`)
	for i := range 200 {
		e.hub.Publish(events.Event{Seq: int64(1<<40 + i), Type: events.TypeSettingsUpdated, Payload: payload})
	}
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	e.gw.CloseAll(protocol.CloseServerReset, events.ReasonServerReset)
	if n := e.gw.Conns(); n != 0 {
		t.Fatalf("conns after CloseAll = %d, want 0", n)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("close took %v with a non-reading client; want about closeGrace", took)
	}
}

func TestResumeAcrossDeletedEventSendsResync(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	e.channel(t, "pub", protocol.ChannelPublic, "alice")
	seqs := e.append(t, msg("pub"), msg("pub"), msg("pub"), msg("pub"))
	e.exec(t, `DELETE FROM events WHERE seq = ?`, seqs[1])

	params := url.Values{protocol.QueryResume: {strconv.FormatInt(seqs[0], 10)}}
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), params)
	if h := c.hello(); h.Resume != protocol.ResumeResyncRequired {
		t.Fatalf("resume across a hole: hello.resume = %q, want resync_required", h.Resume)
	}
	// No partial replay: the only persisted frame before a live marker is none.
	if got := c.eventsUntil(marker(t, e)); len(got) != 0 {
		t.Fatalf("partial replay after resync_required: %v", got)
	}
}

func TestResumeWithoutHoleReplaysInOrder(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	e.channel(t, "pub", protocol.ChannelPublic, "alice")
	seqs := e.append(t, msg("pub"), msg("pub"), msg("pub"), msg("pub"))

	params := url.Values{protocol.QueryResume: {strconv.FormatInt(seqs[0], 10)}}
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), params)
	if h := c.hello(); h.Resume != protocol.ResumeOK {
		t.Fatalf("hello.resume = %q, want ok", h.Resume)
	}
	got := c.eventsUntil(seqs[3])
	if len(got) != 2 || got[0] != seqs[1] || got[1] != seqs[2] {
		t.Fatalf("replayed %v, want %v", got, seqs[1:3])
	}
}

// waitGoroutines polls until the goroutine count is at most want and returns
// it, failing the test after five seconds.
func waitGoroutines(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= want {
			return n
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after closing every connection, want <= %d", n, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestClosedConnectionsLeaveNoGoroutines(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	open := func(i int) {
		t.Helper()
		c := e.dial(t, e.token("alice", auth.RoleMember, fmt.Sprintf("s%d", i)), nil)
		c.hello()
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	}
	open(0) // warm up lazily started goroutines before the baseline
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const conns = 20
	for i := 1; i <= conns; i++ {
		open(i)
	}
	waitGoroutines(t, baseline)
}
