package wsgateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// fakeAuth maps the X-Test-Token header to a principal.
type fakeAuth struct {
	mu sync.Mutex
	m  map[string]*auth.Principal
}

func (f *fakeAuth) Authenticate(r *http.Request) (*auth.Principal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m[r.Header.Get("X-Test-Token")], nil
}

type env struct {
	db   *store.DB
	hub  *events.Hub
	gw   *Gateway
	srv  *httptest.Server
	auth *fakeAuth
}

func newEnv(t *testing.T, o Options) *env {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), store.Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := &env{db: db, auth: &fakeAuth{m: map[string]*auth.Principal{}}}
	e.hub = events.NewHub(events.HubOptions{DB: db})
	o.Hub, o.DB, o.Version = e.hub, db, "1.2.3-test"
	e.gw = New(o)
	e.srv = httptest.NewServer(auth.Middleware(e.auth)(e.gw))
	t.Cleanup(func() {
		e.gw.CloseAll(int(websocket.StatusGoingAway), "test over")
		e.srv.Close()
		e.hub.Close()
		db.Close()
	})
	return e
}

func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if err := e.db.Do(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// user creates an account and a token (= session id) that authenticates as it.
func (e *env) user(t *testing.T, id string, role auth.Role) {
	t.Helper()
	e.exec(t, `INSERT INTO users (id, username, kind, role, created_at) VALUES (?, ?, 'human', ?, 0)`, id, id, string(role))
}

func (e *env) token(id string, role auth.Role, session string) string {
	tok := id + "/" + session
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	e.auth.m[tok] = &auth.Principal{
		User:    &auth.User{ID: id, Username: id, Kind: auth.KindHuman, Role: role},
		Session: &auth.Session{ID: session, UserID: id},
		Via:     auth.ViaCookie,
	}
	return tok
}

func (e *env) channel(t *testing.T, id, kind string, members ...string) {
	t.Helper()
	var name any
	if kind == protocol.ChannelPublic || kind == protocol.ChannelPrivate {
		name = id
	}
	e.exec(t, `INSERT INTO channels (id, kind, name, created_at) VALUES (?, ?, ?, 0)`, id, kind, name)
	for _, m := range members {
		e.exec(t, `INSERT INTO memberships (channel_id, user_id, joined_at) VALUES (?, ?, 0)`, id, m)
	}
}

func (e *env) append(t *testing.T, evs ...events.Event) []int64 {
	t.Helper()
	var seqs []int64
	if err := e.db.Do(context.Background(), func(tx *store.Tx) error {
		seqs = seqs[:0]
		for _, ev := range evs {
			seq, err := events.Append(tx, ev)
			if err != nil {
				return err
			}
			seqs = append(seqs, seq)
		}
		return nil
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	return seqs
}

func (e *env) instance(t *testing.T) (string, int64) {
	t.Helper()
	var id string
	var epoch int64
	if err := e.db.Read().QueryRow(`SELECT id, epoch FROM instance`).Scan(&id, &epoch); err != nil {
		t.Fatal(err)
	}
	return id, epoch
}

func (e *env) url(params url.Values) string {
	return "ws" + strings.TrimPrefix(e.srv.URL, "http") + protocol.WSPath + "?" + params.Encode()
}

func (e *env) origin() string { return e.srv.URL }

// dial connects with token and the server's own Origin.
func (e *env) dial(t *testing.T, token string, params url.Values) *client {
	t.Helper()
	h := http.Header{}
	h.Set("X-Test-Token", token)
	h.Set("Origin", e.origin())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, e.url(params), &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ws.SetReadLimit(1 << 20)
	c := &client{t: t, ws: ws}
	t.Cleanup(func() { ws.CloseNow() })
	return c
}

type client struct {
	t  *testing.T
	ws *websocket.Conn
}

// frame is a decoded server frame: hello, pong or an event envelope.
type frame struct {
	protocol.Event
	raw []byte
}

func (c *client) next() (frame, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return frame{}, err
	}
	var f frame
	f.raw = data
	if err := json.Unmarshal(data, &f.Event); err != nil {
		c.t.Fatalf("decode frame %s: %v", data, err)
	}
	return f, nil
}

func (c *client) mustNext() frame {
	c.t.Helper()
	f, err := c.next()
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return f
}

func (c *client) hello() protocol.Hello {
	c.t.Helper()
	f := c.mustNext()
	var h protocol.Hello
	if err := json.Unmarshal(f.raw, &h); err != nil || h.Type != protocol.FrameHello {
		c.t.Fatalf("first frame %s is not hello", f.raw)
	}
	return h
}

// events reads until the frame with seq marker and returns the seqs of
// the persisted events before it (ephemeral frames skipped).
func (c *client) eventsUntil(marker int64) []int64 {
	c.t.Helper()
	var seqs []int64
	for {
		f := c.mustNext()
		if f.Seq == marker {
			return seqs
		}
		if f.Seq > 0 {
			seqs = append(seqs, f.Seq)
		}
	}
}

// closeCode reads until the socket closes and returns the close code.
func (c *client) closeCode() websocket.StatusCode {
	c.t.Helper()
	for {
		_, err := c.next()
		if err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func (c *client) send(v any) {
	c.t.Helper()
	data, _ := json.Marshal(v)
	if err := c.ws.Write(context.Background(), websocket.MessageText, data); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

func marker(t *testing.T, e *env) int64 {
	t.Helper()
	return e.append(t, events.Event{Type: events.TypeSettingsUpdated, Payload: json.RawMessage(`{"keys":["marker"]}`)})[0]
}

func msg(channel string) events.Event {
	return events.Event{Type: events.TypeMessageCreated, ChannelID: channel, Payload: json.RawMessage(`{"id":"m"}`)}
}

func TestUpgradeRefusals(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	tok := e.token("alice", auth.RoleMember, "s1")
	tests := []struct {
		name   string
		token  string
		origin string
		query  string
		status int
	}{
		{"cross-site origin", tok, "http://evil.example", "", http.StatusForbidden},
		{"https origin for other host", tok, "https://bubbles.local:28225", "", http.StatusForbidden},
		{"no origin", tok, "", "", http.StatusForbidden},
		{"not signed in", "", e.origin(), "", http.StatusUnauthorized},
		{"bad resume", tok, e.origin(), "resume=-1", http.StatusBadRequest},
		{"bad epoch", tok, e.origin(), "epoch=x", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Test-Token", tt.token)
			if tt.origin != "" {
				h.Set("Origin", tt.origin)
			}
			u := "ws" + strings.TrimPrefix(e.srv.URL, "http") + protocol.WSPath + "?" + tt.query
			ws, resp, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{HTTPHeader: h})
			if err == nil {
				ws.CloseNow()
				t.Fatal("upgrade succeeded")
			}
			if resp == nil || resp.StatusCode != tt.status {
				t.Fatalf("response %v, want status %d", resp, tt.status)
			}
		})
	}
	if n := e.gw.Conns(); n != 0 {
		t.Fatalf("open conns = %d", n)
	}
}

func TestHelloResumeOutcomes(t *testing.T) {
	// Each case gets a log with seqs 1-3 pruned (older than retention) and
	// 4-5 retained, so the head is 5.
	const head = 5
	same := func(inst string, epoch int64, resume string) url.Values {
		return url.Values{"resume": {resume}, "instance": {inst}, "epoch": {strconv.FormatInt(epoch, 10)}}
	}
	tests := []struct {
		name   string
		params func(inst string, epoch int64) url.Values
		want   string
		replay int
	}{
		{"fresh connection", func(string, int64) url.Values { return nil }, protocol.ResumeOK, 0},
		{"current state", func(i string, e int64) url.Values { return same(i, e, "5") }, protocol.ResumeOK, 0},
		{"replay retained", func(i string, e int64) url.Values { return same(i, e, "3") }, protocol.ResumeOK, 2},
		{"resume older than retention", func(i string, e int64) url.Values { return same(i, e, "1") }, protocol.ResumeResyncRequired, 0},
		{"wrong epoch", func(i string, e int64) url.Values { return same(i, e+1, "3") }, protocol.ResumeResyncRequired, 0},
		{"wrong instance", func(_ string, e int64) url.Values { return same("01OTHERINSTANCE", e, "3") }, protocol.ResumeInstanceChanged, 0},
		{"resume from the future", func(string, int64) url.Values { return url.Values{"resume": {"9"}} }, protocol.ResumeResyncRequired, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Options{})
			e.user(t, "alice", auth.RoleMember)
			inst, epoch := e.instance(t)
			old := time.Now().Add(-events.Retention - time.Hour).UnixMilli()
			for range 3 {
				e.append(t, events.Event{Type: events.TypeSettingsUpdated, CreatedAt: old})
			}
			if _, err := events.Prune(context.Background(), e.db, time.Now()); err != nil {
				t.Fatal(err)
			}
			e.append(t, events.Event{Type: events.TypeSettingsUpdated}, events.Event{Type: events.TypeSettingsUpdated})

			c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), tt.params(inst, epoch))
			h := c.hello()
			want := protocol.Hello{Type: "hello", InstanceID: inst, Epoch: epoch, SeqHead: head, UserID: "alice", ServerVersion: "1.2.3-test", Resume: tt.want}
			if h != want {
				t.Fatalf("hello = %+v, want %+v", h, want)
			}
			m := marker(t, e)
			if got := c.eventsUntil(m); len(got) != tt.replay {
				t.Fatalf("replayed %v, want %d events", got, tt.replay)
			}
		})
	}
}

func TestResumeAfter1000EventsNoGapNoDuplicate(t *testing.T) {
	e := newEnv(t, Options{})
	for _, u := range []string{"alice", "bob", "carol"} {
		e.user(t, u, auth.RoleMember)
	}
	e.user(t, "gus", auth.RoleGuest)
	e.channel(t, "pub", protocol.ChannelPublic, "alice", "bob")
	e.channel(t, "priv", protocol.ChannelPrivate, "bob")
	e.channel(t, "dm", protocol.ChannelDM, "bob", "carol")
	e.channel(t, "gdm", protocol.ChannelGroupDM, "bob", "carol", "gus")

	// mix cycles through events alice may and may not see.
	mix := func(i int) (events.Event, bool) {
		switch i % 6 {
		case 0:
			return msg("pub"), true
		case 1:
			return msg("priv"), false
		case 2:
			return msg("dm"), false
		case 3:
			return events.Event{Type: events.TypeReadUpdated, UserID: "alice"}, true
		case 4:
			return events.Event{Type: events.TypeReadUpdated, UserID: "bob"}, false
		default:
			return events.Event{Type: events.TypeReactionAdded, ChannelID: "gdm"}, false
		}
	}
	var mu sync.Mutex
	visible := map[int64]bool{}
	write := func(from, n, perTx int) {
		for i := from; i < from+n; i += perTx {
			var batch []events.Event
			var vis []bool
			for j := i; j < min(i+perTx, from+n); j++ {
				ev, ok := mix(j)
				batch = append(batch, ev)
				vis = append(vis, ok)
			}
			seqs := e.append(t, batch...)
			mu.Lock()
			for k, s := range seqs {
				visible[s] = vis[k]
			}
			mu.Unlock()
		}
	}
	write(0, 1000, 25)
	const resume = 100

	inst, epoch := e.instance(t)
	var wg sync.WaitGroup
	wg.Go(func() { write(1000, 150, 1) }) // concurrent live writes
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), url.Values{
		"resume": {strconv.Itoa(resume)}, "instance": {inst}, "epoch": {strconv.FormatInt(epoch, 10)},
	})
	if h := c.hello(); h.Resume != protocol.ResumeOK {
		t.Fatalf("hello = %+v", h)
	}
	wg.Wait()
	m := marker(t, e)
	got := c.eventsUntil(m)

	var want []int64
	for seq := int64(resume + 1); seq < m; seq++ {
		if visible[seq] {
			want = append(want, seq)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got seq %d, want %d (gap, duplicate or leak)", i, got[i], want[i])
		}
	}

	// A guest member of the group DM replays only that conversation.
	g := e.dial(t, e.token("gus", auth.RoleGuest, "s2"), url.Values{"resume": {"0"}})
	g.hello()
	m2 := marker(t, e)
	for _, seq := range g.eventsUntil(m2) {
		ev, _ := mix(int(seq - 1))
		if ev.ChannelID != "gdm" && seq != m {
			t.Fatalf("guest replayed seq %d (%s %s)", seq, ev.Type, ev.ChannelID)
		}
	}
}

func TestCloseAllClosesEverySocketWithCode(t *testing.T) {
	e := newEnv(t, Options{})
	var clients []*client
	for i, u := range []string{"alice", "bob", "carol"} {
		e.user(t, u, auth.RoleMember)
		c := e.dial(t, e.token(u, auth.RoleMember, "s"+strconv.Itoa(i)), nil)
		c.hello()
		clients = append(clients, c)
	}
	codes := make(chan websocket.StatusCode, len(clients))
	for _, c := range clients {
		go func() { codes <- c.closeCode() }()
	}
	e.gw.CloseAll(protocol.CloseServerReset, events.ReasonServerReset)
	for range clients {
		if code := <-codes; code != protocol.CloseServerReset {
			t.Fatalf("close code %d, want 4010", code)
		}
	}
	if n := e.gw.Conns(); n != 0 {
		t.Fatalf("conns after CloseAll = %d", n)
	}
}

func TestSlowConsumerClosedWith4008(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	// The client stops reading; large frames fill the socket buffers, then
	// the 256-frame queue.
	payload := json.RawMessage(`{"keys":["` + strings.Repeat("x", 32<<10) + `"]}`)
	seq := int64(0)
	deadline := time.Now().Add(20 * time.Second)
	for e.hub.Subscribers("alice") > 0 {
		if time.Now().After(deadline) {
			t.Fatal("slow consumer never dropped")
		}
		for range 64 {
			seq++
			e.hub.Publish(events.Event{Seq: seq, Type: events.TypeSettingsUpdated, Payload: payload})
		}
		time.Sleep(5 * time.Millisecond)
	}
	var last int64
	for {
		f, err := c.next()
		if err != nil {
			if code := websocket.CloseStatus(err); code != protocol.CloseSlowConsumer {
				t.Fatalf("close code %d (%v), want 4008", code, err)
			}
			break
		}
		if f.Seq > 0 {
			if f.Seq != last+1 {
				t.Fatalf("gap before close: seq %d after %d", f.Seq, last)
			}
			last = f.Seq
		}
	}
	if last >= seq {
		t.Fatalf("received every frame (%d); expected the overflow to cut the stream", last)
	}
}

func TestSessionRevokedAndDeactivationCloseSockets(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	e.user(t, "bob", auth.RoleMember)
	a1 := e.dial(t, e.token("alice", auth.RoleMember, "sess-1"), nil)
	a2 := e.dial(t, e.token("alice", auth.RoleMember, "sess-2"), nil)
	a3 := e.dial(t, e.token("alice", auth.RoleMember, "sess-3"), nil)
	b := e.dial(t, e.token("bob", auth.RoleMember, "sess-9"), nil)
	for _, c := range []*client{a1, a2, a3, b} {
		c.hello()
	}

	// One session revoked: only its socket closes.
	e.append(t, events.Event{Type: events.TypeSessionRevoked, UserID: "alice", Payload: json.RawMessage(`{"session_id":"sess-1"}`)})
	if code := a1.closeCode(); code != protocol.CloseSessionRevoked {
		t.Fatalf("revoked socket closed with %d", code)
	}
	m := marker(t, e)
	a2.eventsUntil(m)
	a3.eventsUntil(m)

	// Deactivation (sessions revoked + user.updated in one transaction)
	// closes every remaining socket of the user within 1 s.
	codes := make(chan websocket.StatusCode, 2)
	for _, c := range []*client{a2, a3} {
		go func() { codes <- c.closeCode() }()
	}
	start := time.Now()
	if err := e.db.Do(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET deactivated_at = ? WHERE id = 'alice'`, tx.Now()); err != nil {
			return err
		}
		if err := auth.RevokeSessionsTx(tx, "alice", nil); err != nil {
			return err
		}
		_, err := events.Append(tx, events.Event{Type: events.TypeUserUpdated, UserID: "alice", Payload: json.RawMessage(`{"id":"alice","deactivated":true}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case code := <-codes:
			if code != protocol.CloseSessionRevoked {
				t.Fatalf("deactivated socket closed with %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("socket of deactivated user still open after 1s")
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("closing took %v", d)
	}
	m = marker(t, e)
	b.eventsUntil(m) // bob is unaffected
}

func TestDeactivationAloneClosesSockets(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	done := make(chan websocket.StatusCode, 1)
	go func() { done <- c.closeCode() }()
	e.append(t, events.Event{Type: events.TypeUserUpdated, UserID: "alice", Payload: json.RawMessage(`{"id":"alice","deactivated":true}`)})
	select {
	case code := <-done:
		if code != protocol.CloseSessionRevoked {
			t.Fatalf("close code %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("not closed within 1s")
	}
}

func TestClientFrames(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	e.user(t, "bob", auth.RoleMember)
	e.channel(t, "pub", protocol.ChannelPublic, "alice", "bob")
	a := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	b := e.dial(t, e.token("bob", auth.RoleMember, "s2"), nil)
	a.hello()
	b.hello()

	a.send(protocol.ClientFrame{Type: protocol.ClientPing})
	for {
		f := a.mustNext()
		if f.Type == protocol.FramePong {
			break
		}
	}
	ch := "pub"
	a.send(protocol.ClientFrame{Type: protocol.ClientTyping, ChannelID: &ch})
	for {
		f := b.mustNext()
		if f.Type != events.TypeTyping {
			continue
		}
		var p protocol.TypingEvent
		_ = json.Unmarshal(f.Data, &p)
		if f.Seq != 0 || f.ChannelID != "pub" || p.UserID != "alice" {
			t.Fatalf("typing frame %s", f.raw)
		}
		break
	}
	visible := true
	a.send(protocol.ClientFrame{Type: protocol.ClientFocus, ChannelID: &ch, Visible: &visible})
	a.send(protocol.ClientFrame{Type: protocol.ClientPresence, State: protocol.PresenceIdle})
	deadline := time.Now().Add(5 * time.Second)
	for !e.hub.UserFocusedRecently("alice", time.Minute) {
		if time.Now().After(deadline) {
			t.Fatal("focus frame not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}

	for _, bad := range []string{`{"type":"subscribe"}`, `{"type":"typing"}`, `not json`, `{"type":"ping","extra":1}`} {
		c := e.dial(t, e.token("alice", auth.RoleMember, "s3"), nil)
		c.hello()
		if err := c.ws.Write(context.Background(), websocket.MessageText, []byte(bad)); err != nil {
			t.Fatal(err)
		}
		if code := c.closeCode(); code != websocket.StatusPolicyViolation {
			t.Fatalf("frame %s closed with %d, want 1008", bad, code)
		}
	}
	c := e.dial(t, e.token("alice", auth.RoleMember, "s4"), nil)
	c.hello()
	big := `{"type":"ping","ts":"` + strings.Repeat("x", protocol.MaxClientFrameBytes) + `"}`
	_ = c.ws.Write(context.Background(), websocket.MessageText, []byte(big))
	if code := c.closeCode(); code != websocket.StatusMessageTooBig {
		t.Fatalf("oversized frame closed with %d, want 1009", code)
	}
}

func TestUnansweredPingsCloseTheSocket(t *testing.T) {
	e := newEnv(t, Options{PingInterval: 50 * time.Millisecond, DeadAfter: 200 * time.Millisecond})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello() // then stop reading: pings go unanswered
	deadline := time.Now().Add(5 * time.Second)
	for e.gw.Conns() > 0 {
		if time.Now().After(deadline) {
			t.Fatal("dead socket was not closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		_, err := c.next()
		if err == nil {
			continue // frames buffered before the close
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("read after server close = %v", err)
		}
		break
	}
}

func TestReadingClientStaysAlive(t *testing.T) {
	e := newEnv(t, Options{PingInterval: 30 * time.Millisecond, DeadAfter: 150 * time.Millisecond})
	e.user(t, "alice", auth.RoleMember)
	c := e.dial(t, e.token("alice", auth.RoleMember, "s1"), nil)
	c.hello()
	ctx, cancel := context.WithCancel(context.Background())
	readErr := make(chan error, 1)
	go func() {
		for {
			// Reading answers the server pings.
			if _, _, err := c.ws.Read(ctx); err != nil {
				readErr <- err
				return
			}
		}
	}()
	time.Sleep(600 * time.Millisecond) // several DeadAfter periods
	select {
	case err := <-readErr:
		t.Fatalf("responsive client was disconnected: %v", err)
	default:
	}
	if n := e.gw.Conns(); n != 1 {
		t.Fatalf("conns = %d, want 1", n)
	}
	cancel()
	<-readErr
}
