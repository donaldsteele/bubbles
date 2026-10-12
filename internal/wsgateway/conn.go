package wsgateway

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// errClosing aborts the start-up phase when a close was requested.
var errClosing = errors.New("connection is closing")

// closeGrace is how long a requested close may wait for a blocked write or for
// the client's close answer before the connection is torn down.
const closeGrace = time.Second

// conn is one served WebSocket connection.
type conn struct {
	g         *Gateway
	ws        *websocket.Conn
	viewer    events.Viewer
	sessionID string
	q         query
	sub       *events.Subscription

	closeOnce   sync.Once
	closing     chan struct{}
	closeCode   int
	closeReason string

	done chan struct{} // closed by the gateway once the handler finished
}

func newConn(g *Gateway, ws *websocket.Conn, p *auth.Principal, q query) *conn {
	c := &conn{
		g:       g,
		ws:      ws,
		viewer:  viewerFor(p),
		q:       q,
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	if p.Session != nil {
		c.sessionID = p.Session.ID
	}
	return c
}

// requestClose asks the connection to close with code and reason; the
// first request wins. It never blocks (the hub calls it).
func (c *conn) requestClose(code int, reason string) {
	c.closeOnce.Do(func() {
		c.closeCode, c.closeReason = code, reason
		close(c.closing)
	})
}

// serve runs the connection: subscribe (live events start buffering), send
// hello, replay, send the presence snapshot, then stream live events while a
// reader goroutine handles client frames and a pinger checks liveness.
func (c *conn) serve(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		_ = c.ws.CloseNow()
		wg.Wait()
	}()

	c.ws.SetReadLimit(protocol.MaxClientFrameBytes)
	c.sub = c.g.o.Hub.Subscribe(c.viewer, c.sessionID, c.requestClose)
	defer c.sub.Leave()

	readDone := make(chan struct{})
	wg.Add(3)
	go func() {
		defer wg.Done()
		defer close(readDone)
		c.readLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		c.pingLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		c.abortAfterClose(ctx, cancel)
	}()

	head, err := c.start(ctx)
	if err != nil {
		switch {
		case errors.Is(err, errClosing):
			c.finishClose()
		case errors.Is(err, errReplayGap):
			// The log lost an event under the replay: the client reconnects and
			// its resume point then yields resync_required.
			_ = c.ws.Close(websocket.StatusTryAgainLater, "resync required")
		case ctx.Err() == nil:
			c.g.o.Logger.Warn("realtime: start connection", "err", err)
			_ = c.ws.Close(websocket.StatusInternalError, "internal error")
		}
		return
	}
	c.writeLoop(ctx, head, readDone)
}

// abortAfterClose tears the connection down closeGrace after a close was
// requested. A writer blocked on a client that stopped reading, or a close
// handshake that the client never answers, cannot finish on its own; the
// library drops the socket once the connection context is cancelled. A close
// that completes within the grace is unaffected, so its close code still
// reaches a client that is reading.
func (c *conn) abortAfterClose(ctx context.Context, cancel context.CancelFunc) {
	select {
	case <-c.closing:
	case <-ctx.Done():
		return
	}
	t := time.NewTimer(closeGrace)
	defer t.Stop()
	select {
	case <-t.C:
		cancel()
	case <-ctx.Done():
	}
}

// start sends hello, the replay and the presence snapshot, and returns the
// sequence head the replay covered: live frames at or below it are
// duplicates and are skipped.
func (c *conn) start(ctx context.Context) (int64, error) {
	st, err := loadState(ctx, c.g.o.DB.Read())
	if err != nil {
		return 0, err
	}
	outcome, replay := resumeOutcome(c.q, st)
	if replay {
		// Decided before hello is sent: a hole in the range means resync.
		whole, err := retained(ctx, c.g.o.DB.Read(), c.q.resume, st.head)
		if err != nil {
			return 0, err
		}
		if !whole {
			outcome, replay = protocol.ResumeResyncRequired, false
		}
	}
	hello, err := json.Marshal(protocol.Hello{
		Type: protocol.FrameHello, InstanceID: st.instanceID, Epoch: st.epoch, SeqHead: st.head,
		UserID: c.viewer.UserID, ServerVersion: c.g.o.Version, Resume: outcome,
	})
	if err != nil {
		return 0, err
	}
	if err := c.write(ctx, hello); err != nil {
		return 0, err
	}
	viewer := c.sub.Viewer()
	if replay {
		if err := c.replay(ctx, viewer, c.q.resume, st.head); err != nil {
			return 0, err
		}
	}
	for _, f := range c.g.o.Hub.PresenceSnapshot(viewer) {
		if err := c.write(ctx, f.Data); err != nil {
			return 0, err
		}
	}
	return st.head, nil
}

// writeLoop streams queued live frames until the connection ends. A
// requested close always wins over pending frames.
func (c *conn) writeLoop(ctx context.Context, head int64, readDone <-chan struct{}) {
	frames := c.sub.Frames()
	for {
		select {
		case <-c.closing:
			c.finishClose()
			return
		default:
		}
		select {
		case <-c.closing:
			c.finishClose()
			return
		case <-readDone:
			select {
			case <-c.closing:
				c.finishClose()
			default:
			}
			return
		case <-ctx.Done():
			return
		case f := <-frames:
			if f.Seq > 0 && f.Seq <= head {
				continue
			}
			if err := c.write(ctx, f.Data); err != nil {
				select {
				case <-c.closing:
					c.finishClose()
				default:
				}
				return
			}
		}
	}
}

// write sends one text frame; a write that cannot finish within DeadAfter
// kills the connection. It refuses once a close was requested, so no frame
// follows a gap.
func (c *conn) write(ctx context.Context, data []byte) error {
	select {
	case <-c.closing:
		return errClosing
	default:
	}
	wctx, cancel := context.WithTimeout(ctx, c.g.o.DeadAfter)
	defer cancel()
	return c.ws.Write(wctx, websocket.MessageText, data)
}

// finishClose sends the requested close frame and waits (bounded by the
// library) for the client's answer.
func (c *conn) finishClose() {
	_ = c.ws.Close(websocket.StatusCode(c.closeCode), c.closeReason)
}

// readLoop handles client frames until the socket fails or closes. Binary
// or invalid frames close the socket with 1008, as does a client that exceeds
// the frame rate limit; oversized ones are closed by the library with 1009.
func (c *conn) readLoop(ctx context.Context) {
	limit := newTokenBucket(time.Now())
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		if !limit.take(time.Now()) {
			c.requestClose(int(websocket.StatusPolicyViolation), "rate limited")
			return
		}
		if typ != websocket.MessageText {
			c.requestClose(int(websocket.StatusPolicyViolation), "text frames only")
			return
		}
		f, err := parseClientFrame(data)
		if err != nil {
			c.requestClose(int(websocket.StatusPolicyViolation), "invalid frame")
			return
		}
		switch f.Type {
		case protocol.ClientTyping:
			c.sub.Typing(*f.ChannelID)
		case protocol.ClientPresence:
			c.sub.SetPresence(f.State)
		case protocol.ClientFocus:
			c.sub.Focus(*f.Visible)
		case protocol.ClientPing:
			pong, _ := json.Marshal(protocol.Pong{Type: protocol.FramePong})
			if err := c.write(ctx, pong); err != nil {
				return
			}
		}
	}
}

// pingLoop pings every PingInterval; a ping unanswered by DeadAfter after
// the previous pong kills the connection.
func (c *conn) pingLoop(ctx context.Context) {
	interval, timeout := c.g.o.PingInterval, c.g.o.DeadAfter-c.g.o.PingInterval
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		err := c.ws.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				_ = c.ws.CloseNow()
			}
			return
		}
	}
}
