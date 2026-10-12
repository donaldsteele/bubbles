package apps

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

type fakeRT struct {
	mu     sync.Mutex
	online map[string]int
	sent   []events.Event
}

func (f *fakeRT) Publish(e events.Event) {
	f.mu.Lock()
	f.sent = append(f.sent, e)
	f.mu.Unlock()
}

func (f *fakeRT) Subscribers(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[id]
}

func (f *fakeRT) setOnline(id string, n int) {
	f.mu.Lock()
	f.online[id] = n
	f.mu.Unlock()
}

func (f *fakeRT) take() []events.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

type cmdEnv struct {
	*env
	rt *fakeRT
}

func newCmdEnv(t *testing.T) *cmdEnv {
	e := newEnv(t)
	rt := &fakeRT{online: map[string]int{}}
	e.svc = New(Options{DB: e.db, Chat: e.chat, Now: e.clock.Now, Realtime: rt})
	return &cmdEnv{env: e, rt: rt}
}

func grant(a protocol.App, scopes ...string) *auth.TokenGrant {
	return &auth.TokenGrant{ID: "tok-" + a.ID, AppID: a.ID, Scopes: scopes}
}

func (e *cmdEnv) member(name string) chat.Actor {
	return chat.Actor{ID: e.addUser(name, protocol.RoleMember), Role: protocol.RoleMember, Kind: protocol.KindHuman}
}

// setup makes an app with one command in a channel that has the bot and a member.
func (e *cmdEnv) setup(cmd string) (protocol.App, protocol.Channel, chat.Actor, *auth.TokenGrant) {
	e.t.Helper()
	app := e.app("netdiag")
	u := e.member("alice")
	ch := e.channel("ops", protocol.ChannelPublic, u.ID, app.UserID)
	g := grant(app, ScopeCommands)
	if _, err := e.svc.SetCommands(e.ctx, g, []protocol.CommandInput{{Name: cmd, Description: "d", Usage: "/" + cmd}}); err != nil {
		e.t.Fatalf("SetCommands: %v", err)
	}
	return app, ch, u, g
}

func kindOf(err error) error {
	var te *chat.Error
	if errors.As(err, &te) {
		return te.Kind
	}
	if errors.Is(err, chat.ErrValidation) {
		return chat.ErrValidation
	}
	return err
}

func TestCommandRegistry(t *testing.T) {
	e := newCmdEnv(t)
	a, b := e.app("alpha"), e.app("beta")
	ga, gb := grant(a, ScopeCommands), grant(b, ScopeCommands)
	if _, err := e.svc.SetCommands(e.ctx, ga, []protocol.CommandInput{{Name: "ping"}, {Name: "deploy-now", Description: "x"}}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		g    *auth.TokenGrant
		in   []protocol.CommandInput
		want error
		msg  string
	}{
		{"owned by other app", gb, []protocol.CommandInput{{Name: "ping"}}, chat.ErrConflict, "alpha"},
		{"built-in", gb, []protocol.CommandInput{{Name: "topic"}}, chat.ErrConflict, "built-in"},
		{"bad name", gb, []protocol.CommandInput{{Name: "Ping"}}, chat.ErrValidation, ""},
		{"duplicate in set", gb, []protocol.CommandInput{{Name: "x"}, {Name: "x"}}, chat.ErrValidation, ""},
		{"long description", gb, []protocol.CommandInput{{Name: "x", Description: strings.Repeat("a", 201)}}, chat.ErrValidation, ""},
		{"no scope", grant(b, ScopeMessagesRead), []protocol.CommandInput{{Name: "x"}}, chat.ErrForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.SetCommands(e.ctx, tc.g, tc.in)
			if kindOf(err) != tc.want {
				t.Fatalf("err = %v, want kind %v", err, tc.want)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Fatalf("err %q does not name %q", err, tc.msg)
			}
		})
	}
	many := make([]protocol.CommandInput, 51)
	for i := range many {
		many[i] = protocol.CommandInput{Name: "c" + strings.Repeat("a", i)}
	}
	if _, err := e.svc.SetCommands(e.ctx, gb, many); kindOf(err) != chat.ErrValidation {
		t.Fatalf("51 commands: %v", err)
	}
	// A failed replacement leaves the previous set intact.
	if _, err := e.svc.SetCommands(e.ctx, ga, []protocol.CommandInput{{Name: "ok"}, {Name: "help"}}); err == nil {
		t.Fatal("built-in accepted")
	}
	list, err := e.svc.ListCommands(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range list {
		if !c.Builtin {
			names = append(names, c.Name+"@"+c.AppName)
		}
	}
	if strings.Join(names, ",") != "deploy-now@alpha,ping@alpha" {
		t.Fatalf("app commands = %v", names)
	}
	if len(list) != 6+2 || !list[0].Builtin {
		t.Fatalf("list = %+v", list)
	}
	// Replacing frees names.
	if _, err := e.svc.SetCommands(e.ctx, ga, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetCommands(e.ctx, gb, []protocol.CommandInput{{Name: "ping"}}); err != nil {
		t.Fatalf("freed name: %v", err)
	}
}

func decode[T any](t *testing.T, e events.Event) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCommandInvokeDeliversToOwningApp(t *testing.T) {
	e := newCmdEnv(t)
	app, ch, u, _ := e.setup("scan")
	e.rt.setOnline(app.UserID, 1)
	res, err := e.svc.Invoke(e.ctx, u, protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "/scan", Args: "10.0.0.1"})
	if err != nil || res.InvocationID == "" || res.Builtin {
		t.Fatalf("Invoke = %+v, %v", res, err)
	}
	sent := e.rt.take()
	if len(sent) != 1 || sent[0].Type != events.TypeCommandInvoked || sent[0].UserID != app.UserID || sent[0].Seq != 0 {
		t.Fatalf("sent = %+v", sent)
	}
	p := decode[protocol.CommandInvoked](t, sent[0])
	if p.InvocationID != res.InvocationID || p.Command != "scan" || p.Args != "10.0.0.1" || p.ChannelID != ch.ID || p.User.Username != "alice" {
		t.Fatalf("payload = %+v", p)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'command.invoked'`); n != 0 {
		t.Fatalf("command.invoked stored %d times", n)
	}
}

func TestCommandInvokeOfflineAndRefusals(t *testing.T) {
	e := newCmdEnv(t)
	app, ch, u, _ := e.setup("scan")
	_, err := e.svc.Invoke(e.ctx, u, protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan"})
	if !errors.Is(err, ErrAppOffline) {
		t.Fatalf("offline err = %v", err)
	}
	sent := e.rt.take()
	if len(sent) != 1 || sent[0].Type != events.TypeEphemeralMessage || sent[0].UserID != u.ID {
		t.Fatalf("sent = %+v", sent)
	}
	if m := decode[protocol.EphemeralMessage](t, sent[0]); m.Body != "netdiag is offline" || m.Author != nil {
		t.Fatalf("notice = %+v", m)
	}
	tests := []struct {
		name string
		req  protocol.InvokeCommandRequest
		want error
	}{
		{"unknown command", protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "nope"}, chat.ErrNotFound},
		{"unknown channel", protocol.InvokeCommandRequest{ChannelID: "01JZZZZZZZZZZZZZZZZZZZZZZZ", Command: "scan"}, chat.ErrNotFound},
		{"long args", protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan", Args: strings.Repeat("a", 4001)}, chat.ErrValidation},
		{"bad name", protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "a b"}, chat.ErrValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.svc.Invoke(e.ctx, u, tc.req); kindOf(err) != tc.want {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// A channel the bot is not in.
	lone := e.channel("lone", protocol.ChannelPublic, u.ID)
	e.rt.setOnline(app.UserID, 1)
	if _, err := e.svc.Invoke(e.ctx, u, protocol.InvokeCommandRequest{ChannelID: lone.ID, Command: "scan"}); kindOf(err) != chat.ErrConflict {
		t.Fatalf("bot not in channel: %v", err)
	}
}

func TestCommandRespond(t *testing.T) {
	e := newCmdEnv(t)
	app, ch, u, g := e.setup("scan")
	e.rt.setOnline(app.UserID, 1)
	other := e.app("other")
	res, err := e.svc.Invoke(e.ctx, u, protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan"})
	if err != nil {
		t.Fatal(err)
	}
	e.rt.take()
	msgs, evs := e.count(`SELECT COUNT(*) FROM messages`), e.count(`SELECT COUNT(*) FROM events`)

	if _, err := e.svc.Respond(e.ctx, grant(other, ScopeCommands), res.InvocationID, protocol.RespondCommandRequest{Text: "x"}); kindOf(err) != chat.ErrNotFound {
		t.Fatalf("foreign app: %v", err)
	}
	if _, err := e.svc.Respond(e.ctx, g, "missing", protocol.RespondCommandRequest{Text: "x"}); kindOf(err) != chat.ErrNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := e.svc.Respond(e.ctx, grant(app, ScopeMessagesWrite), res.InvocationID, protocol.RespondCommandRequest{Text: "x"}); kindOf(err) != chat.ErrForbidden {
		t.Fatalf("no scope: %v", err)
	}
	if _, err := e.svc.Respond(e.ctx, g, res.InvocationID, protocol.RespondCommandRequest{Text: " "}); kindOf(err) != chat.ErrValidation {
		t.Fatalf("empty: %v", err)
	}

	out, err := e.svc.Respond(e.ctx, g, res.InvocationID, protocol.RespondCommandRequest{Text: "secret", Ephemeral: true})
	if err != nil || !out.Ephemeral || out.Message != nil {
		t.Fatalf("ephemeral respond = %+v, %v", out, err)
	}
	sent := e.rt.take()
	if len(sent) != 1 || sent[0].Type != events.TypeEphemeralMessage || sent[0].UserID != u.ID || sent[0].Seq != 0 {
		t.Fatalf("sent = %+v", sent)
	}
	if m := decode[protocol.EphemeralMessage](t, sent[0]); m.Body != "secret" || m.Author == nil || m.Author.ID != app.UserID || m.InvocationID != res.InvocationID {
		t.Fatalf("ephemeral = %+v", m)
	}
	if e.count(`SELECT COUNT(*) FROM messages`) != msgs || e.count(`SELECT COUNT(*) FROM events`) != evs {
		t.Fatal("an ephemeral response was stored")
	}

	out, err = e.svc.Respond(e.ctx, g, res.InvocationID, protocol.RespondCommandRequest{Text: "hello @channel"})
	if err != nil || out.Message == nil || out.Message.Author.ID != app.UserID {
		t.Fatalf("public respond = %+v, %v", out, err)
	}
	if e.count(`SELECT COUNT(*) FROM messages WHERE mention_channel = 1`) != 0 {
		t.Fatal("response mentions were not disabled")
	}

	e.clock.Advance(InvocationTTL)
	if _, err := e.svc.Respond(e.ctx, g, res.InvocationID, protocol.RespondCommandRequest{Text: "late"}); kindOf(err) != chat.ErrNotFound {
		t.Fatalf("expired: %v", err)
	}
}

func TestCommandBuiltinNoticeGoesToInvoker(t *testing.T) {
	e := newCmdEnv(t)
	_, ch, u, _ := e.setup("scan")
	res, err := e.svc.Invoke(e.ctx, u, protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "help"})
	if err != nil || !res.Builtin {
		t.Fatalf("help = %+v, %v", res, err)
	}
	sent := e.rt.take()
	if len(sent) != 1 || sent[0].UserID != u.ID {
		t.Fatalf("sent = %+v", sent)
	}
	if m := decode[protocol.EphemeralMessage](t, sent[0]); !strings.Contains(m.Body, "/scan") || !strings.Contains(m.Body, "/topic") {
		t.Fatalf("help body = %q", m.Body)
	}
}
