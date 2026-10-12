package bubblesclient

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Synthetic event types emitted by Events (never sent by the server). They
// tell the app that its cached state is stale.
const (
	// EventResyncRequired: events were missed and cannot be replayed (epoch
	// change or pruned history). Re-fetch state from the REST API.
	EventResyncRequired = "sdk.resync_required"
	// EventInstanceChanged: the server's instance id changed (factory reset or
	// a different server). Drop all cached state; the token is likely void.
	EventInstanceChanged = "sdk.instance_changed"
)

const (
	eventBuffer       = 64
	eventReadLimit    = 8 << 20
	helloTimeout      = 15 * time.Second
	eventBackoffBase  = 500 * time.Millisecond
	eventBackoffLimit = 30 * time.Second
)

// Events opens the WebSocket stream and returns a channel of server events.
// The connection is re-established automatically (jittered exponential
// backoff) with resume, instance and epoch so no persisted event is lost or
// duplicated; the channel closes when ctx ends or the stream can never
// resume (ErrUnauthorized, ErrServerIdentityChanged; see EventsErr). The
// first connection is made before Events returns, so a bad token or a pin
// mismatch is reported directly.
func (c *Client) Events(ctx context.Context) (<-chan protocol.Event, error) {
	s := &eventStream{c: c, sleep: sleepCtx}
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan protocol.Event, eventBuffer)
	c.setEventsErr(nil)
	go s.run(ctx, conn, out)
	return out, nil
}

// EventsErr returns why the most recent Events stream ended, or nil.
func (c *Client) EventsErr() error {
	c.evMu.Lock()
	defer c.evMu.Unlock()
	return c.evErr
}

func (c *Client) setEventsErr(err error) {
	c.evMu.Lock()
	c.evErr = err
	c.evMu.Unlock()
}

type eventStream struct {
	c        *Client
	sleep    func(context.Context, time.Duration) bool
	instance string
	epoch    int64
	lastSeq  int64
	resuming bool
	pending  []protocol.Event // synthetic events from the latest hello
}

func (s *eventStream) wsURL() string {
	u := *s.c.base
	u.Scheme = "wss"
	u.Path = protocol.WSPath
	q := url.Values{}
	if s.resuming {
		q.Set(protocol.QueryResume, strconv.FormatInt(s.lastSeq, 10))
		if s.instance != "" {
			q.Set(protocol.QueryInstance, s.instance)
		}
		if s.epoch > 0 {
			q.Set(protocol.QueryEpoch, strconv.FormatInt(s.epoch, 10))
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// connect dials, reads the hello frame and updates the resume state.
func (s *eventStream) connect(ctx context.Context) (*websocket.Conn, error) {
	h := http.Header{}
	h.Set("User-Agent", s.c.ua)
	if s.c.token != "" {
		h.Set("Authorization", "Bearer "+s.c.token)
	}
	conn, res, err := websocket.Dial(ctx, s.wsURL(), &websocket.DialOptions{HTTPClient: s.c.http, HTTPHeader: h})
	if err != nil {
		if res != nil {
			return nil, &APIError{Status: res.StatusCode, Code: codeForStatus(res.StatusCode), Message: "websocket upgrade refused"}
		}
		return nil, wrapTransportErr(err)
	}
	conn.SetReadLimit(eventReadLimit)
	hctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	_, data, err := conn.Read(hctx)
	if err != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("%w: read hello: %v", ErrUnavailable, err)
	}
	var hello protocol.Hello
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != protocol.FrameHello {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("%w: first frame was not hello", ErrUnavailable)
	}
	s.instance, s.epoch = hello.InstanceID, hello.Epoch
	switch hello.Resume {
	case protocol.ResumeResyncRequired:
		s.lastSeq = hello.SeqHead
		s.pending = append(s.pending, protocol.Event{Type: EventResyncRequired})
	case protocol.ResumeInstanceChanged:
		s.lastSeq = hello.SeqHead
		s.pending = append(s.pending, protocol.Event{Type: EventInstanceChanged})
	default:
		if !s.resuming {
			s.lastSeq = hello.SeqHead
		}
	}
	s.resuming = true
	return conn, nil
}

func (s *eventStream) run(ctx context.Context, conn *websocket.Conn, out chan<- protocol.Event) {
	defer close(out)
	failures := 0
	for {
		err := s.pump(ctx, conn, out)
		_ = conn.CloseNow()
		if ctx.Err() != nil {
			return
		}
		switch websocket.CloseStatus(err) {
		case websocket.StatusCode(protocol.CloseSessionRevoked), websocket.StatusCode(protocol.CloseServerReset):
			s.c.setEventsErr(ErrUnauthorized)
			return
		case websocket.StatusCode(protocol.CloseSlowConsumer):
			failures = 0 // reconnect at once with resume
		default:
			failures++
		}
		for {
			if failures > 0 && !s.sleep(ctx, backoff(failures)) {
				return
			}
			next, err := s.connect(ctx)
			if err == nil {
				conn = next
				failures = 0 // the next disconnect backs off from the minimum
				break
			}
			if ctx.Err() != nil {
				return
			}
			if fatal(err) {
				s.c.setEventsErr(err)
				return
			}
			failures++
		}
	}
}

// pump forwards frames until the connection fails.
func (s *eventStream) pump(ctx context.Context, conn *websocket.Conn, out chan<- protocol.Event) error {
	for _, ev := range s.pending {
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.pending = nil
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &head) != nil || head.Type == protocol.FramePong || head.Type == protocol.FrameHello {
			continue
		}
		var ev protocol.Event
		if json.Unmarshal(data, &ev) != nil {
			continue
		}
		if ev.Seq > 0 {
			if ev.Seq <= s.lastSeq {
				continue
			}
			s.lastSeq = ev.Seq
		}
		select {
		case out <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// backoff returns the jittered delay before reconnect attempt n (n >= 1):
// exponential from 500 ms to 30 s, randomized over the upper half.
func backoff(n int) time.Duration {
	d := eventBackoffBase << min(n-1, 10)
	if d > eventBackoffLimit || d <= 0 {
		d = eventBackoffLimit
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// sleepCtx waits d; it reports false when ctx ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
