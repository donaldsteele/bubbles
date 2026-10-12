package wsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// appToken registers a bearer principal for bot and returns its test token.
func (e *env) appToken(t *testing.T, bot string, scopes, allow []string) string {
	t.Helper()
	e.exec(t, `INSERT INTO users (id, username, kind, role, created_at) VALUES (?, ?, 'app', 'member', 0)`, bot, bot)
	tok := bot + "/bearer"
	e.auth.mu.Lock()
	defer e.auth.mu.Unlock()
	e.auth.m[tok] = &auth.Principal{
		User:  &auth.User{ID: bot, Username: bot, Kind: auth.KindApp, Role: auth.RoleMember},
		Via:   auth.ViaBearer,
		Token: &auth.TokenGrant{ID: "t-" + bot, AppID: "a-" + bot, Scopes: scopes, ChannelIDs: allow},
	}
	return tok
}

// dialBearer connects without an Origin header, as an app does.
func (e *env) dialBearer(t *testing.T, token string) *client {
	t.Helper()
	h := http.Header{}
	h.Set("X-Test-Token", token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, e.url(url.Values{}), &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	ws.SetReadLimit(1 << 20)
	t.Cleanup(func() { ws.CloseNow() })
	c := &client{t: t, ws: ws}
	c.hello()
	return c
}

func invoked(bot string) events.Event {
	return events.Event{Type: events.TypeCommandInvoked, UserID: bot, Payload: json.RawMessage(`{"invocation_id":"i1"}`)}
}

func TestAppEventsWithoutEventsReadSeeOnlyOwnCommands(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	tok := e.appToken(t, "bot", []string{protocol.ScopeCommands}, nil)
	other := e.appToken(t, "bot2", []string{protocol.ScopeCommands}, nil)
	e.channel(t, "c1", protocol.ChannelPublic, "alice", "bot", "bot2")
	app := e.dialBearer(t, tok)
	app2 := e.dialBearer(t, other)

	e.append(t, msg("c1"))
	e.hub.Publish(invoked("bot2"))
	e.hub.Publish(invoked("bot"))

	f := app.mustNext()
	if f.Type != events.TypeCommandInvoked || f.Seq != 0 {
		t.Fatalf("app got %s seq %d, want its own command.invoked", f.Type, f.Seq)
	}
	f = app2.mustNext()
	if f.Type != events.TypeCommandInvoked {
		t.Fatalf("app2 got %s, want command.invoked", f.Type)
	}
}

func TestAppEventsWithEventsReadSeeMemberChannelsOnly(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	scopes := []string{protocol.ScopeEventsRead, protocol.ScopeMessagesRead}
	all := e.appToken(t, "bot", scopes, nil)
	limited := e.appToken(t, "bot2", scopes, []string{"in"})
	e.channel(t, "in", protocol.ChannelPublic, "alice", "bot", "bot2")
	e.channel(t, "listed-out", protocol.ChannelPublic, "alice", "bot", "bot2")
	e.channel(t, "not-member", protocol.ChannelPublic, "alice")
	a := e.dialBearer(t, all)
	b := e.dialBearer(t, limited)

	seqs := e.append(t, msg("not-member"), msg("listed-out"), msg("in"))
	got := func(c *client, last int64) []int64 { return c.eventsUntil(last) }
	if s := got(a, seqs[2]); len(s) != 1 || s[0] != seqs[1] {
		t.Fatalf("unrestricted app saw %v before seq %d, want only the member channel message %d", s, seqs[2], seqs[1])
	}
	if s := got(b, seqs[2]); len(s) != 0 {
		t.Fatalf("allowlisted app saw %v before the allowed message, want none", s)
	}
}

func TestAppEventsWithoutMessagesReadGetNoContent(t *testing.T) {
	e := newEnv(t, Options{})
	e.user(t, "alice", auth.RoleMember)
	tok := e.appToken(t, "bot", []string{protocol.ScopeEventsRead}, nil)
	e.channel(t, "c1", protocol.ChannelPublic, "alice", "bot")
	app := e.dialBearer(t, tok)

	seqs := e.append(t,
		msg("c1"),
		events.Event{Type: events.TypePinAdded, ChannelID: "c1", Payload: json.RawMessage(`{"channel_id":"c1","message_id":"m","user_id":"alice"}`)},
		events.Event{Type: events.TypeAlertUpdated, ChannelID: "c1", Payload: json.RawMessage(`{"transition":"created"}`)},
		events.Event{Type: events.TypeReactionAdded, ChannelID: "c1", Payload: json.RawMessage(`{"message_id":"m","user_id":"alice","emoji":"x","count":1}`)},
	)
	f := app.mustNext()
	for f.Seq == 0 {
		f = app.mustNext()
	}
	if f.Seq != seqs[3] || f.Type != events.TypeReactionAdded {
		t.Fatalf("first frame = %s seq %d, want only the id-only reaction %d", f.Type, f.Seq, seqs[3])
	}
}

func TestViewerForCarriesTokenIdentity(t *testing.T) {
	p := &auth.Principal{
		User:  &auth.User{ID: "bot", Kind: protocol.KindApp, Role: auth.RoleMember},
		Via:   auth.ViaBearer,
		Token: &auth.TokenGrant{ID: "t1", AppID: "a1", Scopes: []string{protocol.ScopeEventsRead}, ExpiresAt: 42},
	}
	v := viewerFor(p)
	if v.TokenID != "t1" || v.AppID != "a1" || v.TokenExpiresAt != 42 {
		t.Fatalf("viewer token identity = %q %q %d", v.TokenID, v.AppID, v.TokenExpiresAt)
	}
	cookie := viewerFor(&auth.Principal{User: &auth.User{ID: "u", Kind: protocol.KindHuman}, Via: auth.ViaCookie})
	if cookie.TokenID != "" || cookie.TokenExpiresAt != 0 {
		t.Fatalf("cookie viewer carries token identity: %+v", cookie)
	}
}
