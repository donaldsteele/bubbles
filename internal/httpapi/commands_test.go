package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// recordingRT is the hub stand-in of the command tests.
type recordingRT struct {
	mu     sync.Mutex
	online map[string]int
	sent   []events.Event
}

func (r *recordingRT) Publish(e events.Event) {
	r.mu.Lock()
	r.sent = append(r.sent, e)
	r.mu.Unlock()
}

func (r *recordingRT) Subscribers(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.online[id]
}

func (r *recordingRT) take() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

func cmdBody(cmds ...string) []protocol.CommandInput {
	var out []protocol.CommandInput
	for _, c := range cmds {
		out = append(out, protocol.CommandInput{Name: c, Description: "does " + c, Usage: "/" + c})
	}
	return out
}

func TestCommandEndpoints(t *testing.T) {
	e := newEnv(t)
	rt := &recordingRT{online: map[string]int{}}
	e.apps.SetRealtime(rt)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.21")
	ch := admin.createChannel(t, "ops", protocol.ChannelPublic, alice)
	b := e.newBot(admin, "netdiag", []string{protocol.ScopeCommands, protocol.ScopeMessagesRead}, nil, ch.ID)
	other := e.newBot(admin, "other", []string{protocol.ScopeCommands}, nil)

	// Registering: bearer only, scope enforced, conflicts name the owner.
	rec := b.do(e, http.MethodPut, "/api/v1/apps/self/commands", cmdBody("scan", "diag"))
	wantStatus(t, rec, http.StatusOK, "")
	if list := decodeBody[protocol.CommandList](t, rec); len(list.Items) != 2 || list.Items[0].AppName != "netdiag" {
		t.Fatalf("stored = %+v", list)
	}
	rec = other.do(e, http.MethodPut, "/api/v1/apps/self/commands", cmdBody("scan"))
	if msg := wantStatus(t, rec, http.StatusConflict, "conflict").Message; !strings.Contains(msg, "netdiag") {
		t.Fatalf("conflict message %q does not name the owner", msg)
	}
	wantStatus(t, other.do(e, http.MethodPut, "/api/v1/apps/self/commands", cmdBody("topic")), http.StatusConflict, "conflict")
	wantStatus(t, other.do(e, http.MethodPut, "/api/v1/apps/self/commands", cmdBody("Bad Name")), http.StatusBadRequest, "bad_request")
	wantStatus(t, alice.do(http.MethodPut, "/api/v1/apps/self/commands", cmdBody("mine")), http.StatusForbidden, "forbidden")
	noScope := e.newBot(admin, "plain", []string{protocol.ScopeMessagesRead}, nil)
	wantStatus(t, noScope.do(e, http.MethodPut, "/api/v1/apps/self/commands", cmdBody("x")), http.StatusForbidden, "forbidden")

	// Listing: built-ins plus app commands, for a person and for a bearer.
	list := decodeBody[protocol.CommandList](t, alice.do(http.MethodGet, "/api/v1/commands", nil))
	var builtin, app int
	for _, c := range list.Items {
		if c.Builtin {
			builtin++
		} else if c.AppName == "netdiag" {
			app++
		}
	}
	if builtin != 6 || app != 2 {
		t.Fatalf("list has %d built-ins and %d netdiag commands", builtin, app)
	}
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/commands", nil), http.StatusOK, "")
	wantStatus(t, other.do(e, http.MethodGet, "/api/v1/commands", nil), http.StatusForbidden, "forbidden")

	// Offline: 503 and an ephemeral notice to the invoker only.
	inv := protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan", Args: "lan"}
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/commands/invoke", inv), http.StatusServiceUnavailable, "unavailable")
	sent := rt.take()
	if len(sent) != 1 || sent[0].Type != events.TypeEphemeralMessage || sent[0].UserID != alice.id {
		t.Fatalf("offline sent = %+v", sent)
	}
	var note protocol.EphemeralMessage
	if err := json.Unmarshal(sent[0].Payload, &note); err != nil || note.Body != "netdiag is offline" {
		t.Fatalf("notice = %+v (%v)", note, err)
	}

	// Online: 202, delivered to the app's user only.
	rt.mu.Lock()
	rt.online[b.app.UserID] = 1
	rt.mu.Unlock()
	rec = alice.do(http.MethodPost, "/api/v1/commands/invoke", inv)
	wantStatus(t, rec, http.StatusAccepted, "")
	res := decodeBody[protocol.InvokedCommand](t, rec)
	sent = rt.take()
	if res.InvocationID == "" || len(sent) != 1 || sent[0].Type != events.TypeCommandInvoked || sent[0].UserID != b.app.UserID {
		t.Fatalf("invoke = %+v sent = %+v", res, sent)
	}

	// Another app cannot answer; the owner can, ephemerally and publicly.
	path := "/api/v1/commands/invocations/" + res.InvocationID + "/respond"
	wantStatus(t, other.do(e, http.MethodPost, path, protocol.RespondCommandRequest{Text: "hijack"}), http.StatusNotFound, "not_found")
	msgs, evs := e.count(`SELECT COUNT(*) FROM messages`), e.count(`SELECT COUNT(*) FROM events`)
	wantStatus(t, b.do(e, http.MethodPost, path, protocol.RespondCommandRequest{Text: "psst", Ephemeral: true}), http.StatusOK, "")
	if sent = rt.take(); len(sent) != 1 || sent[0].UserID != alice.id || sent[0].Type != events.TypeEphemeralMessage {
		t.Fatalf("ephemeral sent = %+v", sent)
	}
	if e.count(`SELECT COUNT(*) FROM messages`) != msgs || e.count(`SELECT COUNT(*) FROM events`) != evs {
		t.Fatal("ephemeral response was stored")
	}
	rec = b.do(e, http.MethodPost, path, protocol.RespondCommandRequest{Text: "all clear"})
	wantStatus(t, rec, http.StatusOK, "")
	if r := decodeBody[protocol.RespondedCommand](t, rec); r.Message == nil || r.Message.Body != "all clear" {
		t.Fatalf("public response = %+v", r)
	}
	wantStatus(t, b.do(e, http.MethodPost, "/api/v1/commands/invocations/nope/respond", protocol.RespondCommandRequest{Text: "x"}), http.StatusNotFound, "not_found")

	// Invoke is for people with a session, and validates its input.
	wantStatus(t, b.do(e, http.MethodPost, "/api/v1/commands/invoke", inv), http.StatusForbidden, "forbidden")
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/commands/invoke",
		protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan", Args: strings.Repeat("a", 4001)}), http.StatusBadRequest, "bad_request")
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/commands/invoke",
		protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "ghost"}), http.StatusNotFound, "not_found")

	// Built-ins: a misuse is an ephemeral notice, not an error.
	rt.take()
	rec = alice.do(http.MethodPost, "/api/v1/commands/invoke", protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "topic", Args: "new topic"})
	wantStatus(t, rec, http.StatusOK, "")
	if sent = rt.take(); len(sent) != 1 || !strings.Contains(string(sent[0].Payload), "channel owner") {
		t.Fatalf("/topic by a member sent = %+v", sent)
	}
	wantStatus(t, alice.do(http.MethodPost, "/api/v1/commands/invoke", protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "me", Args: "waves"}), http.StatusOK, "")
	if e.count(`SELECT COUNT(*) FROM messages WHERE body = '_alice waves_'`) != 1 {
		t.Fatal("/me did not post")
	}
}

func TestCommandInvokeIsRateLimitedPerUser(t *testing.T) {
	e := newEnv(t)
	e.apps.SetRealtime(&recordingRT{online: map[string]int{}})
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.21")
	ch := admin.createChannel(t, "ops", protocol.ChannelPublic, alice)

	// Each attempt is refused as offline (503) until the per-user burst of
	// messagePostBurst runs out; after that the limiter answers 429.
	inv := protocol.InvokeCommandRequest{ChannelID: ch.ID, Command: "scan"}
	var limited int
	for i := 0; i < messagePostBurst+5; i++ {
		rec := alice.do(http.MethodPost, "/api/v1/commands/invoke", inv)
		if rec.Code != http.StatusTooManyRequests {
			continue
		}
		limited++
		if rec.Header().Get("Retry-After") == "" {
			t.Fatal("429 without Retry-After")
		}
	}
	if limited == 0 {
		t.Fatalf("no invoke was rate limited within %d attempts", messagePostBurst+5)
	}
}

// POST /commands/invoke draws from the same per-user bucket as REST message
// posts (and, through the shared limiter, MCP posts of the same user).
func TestCommandInvokeSharesThePostBucket(t *testing.T) {
	setup := func() (member, string) {
		e := newEnv(t)
		e.apps.SetRealtime(&recordingRT{online: map[string]int{}})
		admin := e.newAdmin()
		alice := e.newMember("alice", "192.168.1.21")
		ch := admin.createChannel(t, "ops", protocol.ChannelPublic, alice)
		return alice, ch.ID
	}
	post := func(alice member, chID string) int {
		return alice.do(http.MethodPost, "/api/v1/channels/"+chID+"/messages", protocol.NewMessage{Body: "x"}).Code
	}
	invoke := func(alice member, chID string) int {
		return alice.do(http.MethodPost, "/api/v1/commands/invoke", protocol.InvokeCommandRequest{ChannelID: chID, Command: "scan"}).Code
	}

	alice, chID := setup()
	for i := 0; i < messagePostBurst; i++ {
		if code := post(alice, chID); code != http.StatusCreated {
			t.Fatalf("REST post %d: status %d", i, code)
		}
	}
	if code := invoke(alice, chID); code != http.StatusTooManyRequests {
		t.Errorf("invoke after the REST burst: status %d, want 429", code)
	}

	alice, chID = setup()
	for i := 0; i < messagePostBurst; i++ {
		if code := invoke(alice, chID); code == http.StatusTooManyRequests {
			t.Fatalf("invoke %d limited inside the burst", i)
		}
	}
	if code := post(alice, chID); code != http.StatusTooManyRequests {
		t.Errorf("REST post after the invoke burst: status %d, want 429", code)
	}
}
