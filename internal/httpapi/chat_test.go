package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Every exchange in this file goes through the contract harness: requests and
// responses are validated against docs/openapi.yaml as a side effect.

// member is a signed-in test account.
type member struct {
	*client
	id string
}

func (e *env) newMember(username, ip string) member {
	e.t.Helper()
	return e.memberOf(e.addUser(username, ip))
}

func (e *env) memberOf(c *client) member {
	e.t.Helper()
	rec := c.do(http.MethodGet, "/api/v1/me", nil)
	wantStatus(e.t, rec, http.StatusOK, "")
	return member{client: c, id: decodeBody[protocol.Me](e.t, rec).ID}
}

func (e *env) newAdmin() member { return e.memberOf(e.setup()) }

func (e *env) newGuest(username, ip string) member {
	e.t.Helper()
	_, code, err := e.svc.CreateInvite(e.ctx, audit.Console, auth.CreateInviteInput{Role: auth.RoleGuest})
	if err != nil {
		e.t.Fatal(err)
	}
	c := e.client(ip)
	wantStatus(e.t, c.do(http.MethodPost, "/api/v1/invites/"+code+"/accept",
		protocol.AcceptInviteRequest{Username: username, Password: goodPassword}), http.StatusCreated, "")
	return e.memberOf(c)
}

func (m member) createChannel(t *testing.T, name, kind string, others ...member) protocol.Channel {
	t.Helper()
	req := protocol.CreateChannelRequest{Name: name, Kind: kind}
	for _, o := range others {
		req.MemberIDs = append(req.MemberIDs, o.id)
	}
	rec := m.do(http.MethodPost, "/api/v1/channels", req)
	wantStatus(t, rec, http.StatusCreated, "")
	return decodeBody[protocol.Channel](t, rec)
}

func (m member) post(t *testing.T, channelID, body string) protocol.Message {
	t.Helper()
	rec := m.do(http.MethodPost, "/api/v1/channels/"+channelID+"/messages", protocol.CreateMessageRequest{Body: body})
	wantStatus(t, rec, http.StatusCreated, "")
	return decodeBody[protocol.Message](t, rec)
}

func (m member) reply(t *testing.T, channelID, rootID, body string) protocol.Message {
	t.Helper()
	rec := m.do(http.MethodPost, "/api/v1/channels/"+channelID+"/messages", protocol.CreateMessageRequest{Body: body, ThreadRootID: &rootID})
	wantStatus(t, rec, http.StatusCreated, "")
	return decodeBody[protocol.Message](t, rec)
}

func (e *env) generalID() string {
	e.t.Helper()
	var id string
	if err := e.db.Read().QueryRow(`SELECT id FROM channels WHERE name = 'general'`).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func ids(msgs []protocol.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func wantIDs(t *testing.T, what string, got []protocol.Message, want []string) {
	t.Helper()
	if g := ids(got); !slices.Equal(g, want) {
		t.Errorf("%s: ids = %v, want %v", what, g, want)
	}
}

func TestChatRoutesRequireSignIn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	anon := e.client("")
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/bootstrap"},
		{http.MethodGet, "/users"},
		{http.MethodGet, "/users/x"},
		{http.MethodGet, "/channels"},
		{http.MethodPost, "/channels"},
		{http.MethodPatch, "/channels/x"},
		{http.MethodPost, "/channels/x/archive"},
		{http.MethodPost, "/channels/x/unarchive"},
		{http.MethodPost, "/channels/x/join"},
		{http.MethodPost, "/channels/x/leave"},
		{http.MethodGet, "/channels/x/members"},
		{http.MethodPut, "/channels/x/members/y"},
		{http.MethodDelete, "/channels/x/members/y"},
		{http.MethodPatch, "/channels/x/membership"},
		{http.MethodPost, "/channels/x/read"},
		{http.MethodPost, "/dms"},
		{http.MethodGet, "/channels/x/messages"},
		{http.MethodPost, "/channels/x/messages"},
		{http.MethodGet, "/messages/x/thread"},
		{http.MethodPatch, "/messages/x"},
		{http.MethodDelete, "/messages/x"},
		{http.MethodPut, "/messages/x/reactions/%F0%9F%91%8D"},
		{http.MethodDelete, "/messages/x/reactions/%F0%9F%91%8D"},
		{http.MethodGet, "/channels/x/pins"},
		{http.MethodPut, "/channels/x/pins/y"},
		{http.MethodDelete, "/channels/x/pins/y"},
	} {
		wantStatus(t, anon.do(rt.method, "/api/v1"+rt.path, nil), http.StatusUnauthorized, CodeUnauthorized)
	}
}

func TestChatBootstrap(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	general := e.generalID()

	rec := admin.do(http.MethodGet, "/api/v1/bootstrap", nil)
	wantStatus(t, rec, http.StatusOK, "")
	b := decodeBody[protocol.Bootstrap](t, rec)
	if b.InstanceID == "" || b.Epoch < 1 || b.SeqHead < 1 {
		t.Errorf("instance_id/epoch/seq_head = %q/%d/%d", b.InstanceID, b.Epoch, b.SeqHead)
	}
	if b.Me.Username != "admin" || b.Me.Role != "admin" {
		t.Errorf("me = %+v", b.Me.User)
	}
	if len(b.Users) != 2 {
		t.Errorf("users = %d, want 2", len(b.Users))
	}
	if len(b.Channels) != 1 || b.Channels[0].ID != general || len(b.Memberships) != 1 {
		t.Errorf("channels = %+v, memberships = %+v", b.Channels, b.Memberships)
	}
	if b.SettingsPublic.InstanceName != "Home" {
		t.Errorf("settings_public = %+v", b.SettingsPublic)
	}

	// Another user's post moves seq_head and shows up as unread.
	admin.post(t, general, "hello @admin")
	rec = bob.do(http.MethodGet, "/api/v1/bootstrap", nil)
	b2 := decodeBody[protocol.Bootstrap](t, rec)
	if b2.SeqHead <= b.SeqHead || b2.InstanceID != b.InstanceID || b2.Epoch != b.Epoch {
		t.Errorf("second bootstrap = %q/%d/%d, first %q/%d/%d", b2.InstanceID, b2.Epoch, b2.SeqHead, b.InstanceID, b.Epoch, b.SeqHead)
	}
	if len(b2.Unread) != 1 || b2.Unread[0].ChannelID != general || b2.Unread[0].Unread != 1 {
		t.Errorf("unread = %+v", b2.Unread)
	}
}

func TestChatPostMessageNonceReplay(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	other := admin.createChannel(t, "other", protocol.ChannelPublic)
	path := "/api/v1/channels/" + general + "/messages"

	req := protocol.CreateMessageRequest{Body: "once", Nonce: "n-1"}
	rec := admin.do(http.MethodPost, path, req)
	wantStatus(t, rec, http.StatusCreated, "")
	first := decodeBody[protocol.Message](t, rec)
	if first.Body != "once" || first.Author.Username != "admin" || first.ChannelID != general {
		t.Errorf("message = %+v", first)
	}

	// The retry gets the stored message with 200 and writes nothing.
	req.Body = "edited retry"
	rec = admin.do(http.MethodPost, path, req)
	wantStatus(t, rec, http.StatusOK, "")
	if again := decodeBody[protocol.Message](t, rec); again.ID != first.ID || again.Body != "once" {
		t.Errorf("replay = %+v, want message %s", again, first.ID)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE channel_id = ?`, general); n != 1 {
		t.Errorf("%d messages stored, want 1", n)
	}

	// The same nonce in another channel is a validation error on nonce.
	rec = admin.do(http.MethodPost, "/api/v1/channels/"+other.ID+"/messages", req)
	e400 := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
	if e400.FieldErrors()["nonce"] == "" {
		t.Errorf("details = %+v, want fields.nonce", e400.Details)
	}

	// Nonces are per author.
	bob := e.newMember("bob", "")
	rec = bob.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "mine", Nonce: "n-1"})
	wantStatus(t, rec, http.StatusCreated, "")

	// No nonce: every post is new.
	a := admin.post(t, general, "same")
	b := admin.post(t, general, "same")
	if a.ID == b.ID {
		t.Error("posts without a nonce were merged")
	}
}

func TestChatPostMessageValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	path := "/api/v1/channels/" + general + "/messages"

	e400 := wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "   "}), http.StatusBadRequest, CodeBadRequest)
	if e400.FieldErrors()["body"] == "" {
		t.Errorf("details = %+v, want fields.body", e400.Details)
	}
	wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: strings.Repeat("x", 8001)}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "hi", FileIDs: []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, path, "{not json"), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, path, `{"body":"x"}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/nope/messages", protocol.CreateMessageRequest{Body: "x"}), http.StatusNotFound, CodeNotFound)
}

func TestChatCrossOriginWritesAreForbidden(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	general := e.generalID()
	msg := admin.post(t, general, "keep me")

	evil := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+general+"/messages", protocol.CreateMessageRequest{Body: "forged"}, evil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: "forged"}, evil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/messages/"+msg.ID, nil, evil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPut, "/api/v1/messages/"+msg.ID+"/reactions/"+url.PathEscape("👍"), nil, evil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels", protocol.CreateChannelRequest{Name: "forged", Kind: "public"}, evil), http.StatusForbidden, CodeForbidden)
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE body = 'forged' OR deleted_at IS NOT NULL`); n != 0 {
		t.Errorf("%d messages changed by cross-origin requests", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE name = 'forged'`); n != 0 {
		t.Error("a channel was created by a cross-origin request")
	}
	// Reads need no Origin and still work.
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil, evil), http.StatusOK, "")
}

func TestChatMessageRateLimitPerUser(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	general := e.generalID()
	path := "/api/v1/channels/" + general + "/messages"

	for i := range 20 {
		wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "m" + strconv.Itoa(i)}), http.StatusCreated, "")
	}
	rec := admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "one too many"})
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE author_id = ?`, admin.id); n != 20 {
		t.Errorf("%d admin messages stored, want 20", n)
	}
	// Reads and other users are not affected.
	wantStatus(t, admin.do(http.MethodGet, path, nil), http.StatusOK, "")
	wantStatus(t, bob.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "bob is fine"}), http.StatusCreated, "")
	// The bucket refills at 10 per second.
	e.clock.Advance(100 * time.Millisecond)
	wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "refilled"}), http.StatusCreated, "")
	wantStatus(t, admin.do(http.MethodPost, path, protocol.CreateMessageRequest{Body: "empty again"}), http.StatusTooManyRequests, CodeRateLimited)
}

func TestPaginationBeforeAfterAround(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "history", protocol.ChannelPublic)
	var all []string
	for i := range 12 {
		all = append(all, admin.post(t, ch.ID, "msg "+strconv.Itoa(i)).ID)
	}
	base := "/api/v1/channels/" + ch.ID + "/messages"
	get := func(query string) protocol.MessagePage {
		t.Helper()
		rec := admin.do(http.MethodGet, base+query, nil)
		wantStatus(t, rec, http.StatusOK, "")
		return decodeBody[protocol.MessagePage](t, rec)
	}
	flags := func(what string, p protocol.MessagePage, before, after bool) {
		t.Helper()
		if p.HasMoreBefore != before || p.HasMoreAfter != after {
			t.Errorf("%s: has_more_before/after = %v/%v, want %v/%v", what, p.HasMoreBefore, p.HasMoreAfter, before, after)
		}
	}

	p := get("")
	wantIDs(t, "default", p.Items, all)
	flags("default", p, false, false)

	p = get("?limit=5")
	wantIDs(t, "newest 5", p.Items, all[7:])
	flags("newest 5", p, true, false)

	p = get("?limit=5&before=" + all[7])
	wantIDs(t, "before", p.Items, all[2:7])
	flags("before", p, true, true)

	p = get("?limit=5&before=" + all[5])
	wantIDs(t, "before, last page", p.Items, all[:5])
	flags("before, last page", p, false, true)

	p = get("?limit=5&after=" + all[3])
	wantIDs(t, "after", p.Items, all[4:9])
	flags("after", p, true, true)

	p = get("?limit=5&after=" + all[6])
	wantIDs(t, "after, last page", p.Items, all[7:])
	flags("after, last page", p, true, false)

	p = get("?limit=5&around=" + all[6])
	wantIDs(t, "around", p.Items, all[4:9])
	flags("around", p, true, true)

	p = get("?limit=100&around=" + all[0])
	wantIDs(t, "around the oldest", p.Items, all)
	flags("around the oldest", p, false, false)

	p = get("?limit=1")
	wantIDs(t, "limit 1", p.Items, all[11:])

	// Walking back page by page covers everything exactly once.
	var walked []string
	cursor := ""
	for {
		q := "?limit=5"
		if cursor != "" {
			q += "&before=" + cursor
		}
		pg := get(q)
		walked = append(ids(pg.Items), walked...)
		if !pg.HasMoreBefore {
			break
		}
		cursor = pg.Items[0].ID
	}
	if !slices.Equal(walked, all) {
		t.Errorf("walk = %v, want %v", walked, all)
	}

	// Invalid parameters are 400 with details.fields; an unknown anchor in
	// around is 404.
	for _, tc := range []struct {
		name, query, field string
	}{
		{"before and after", "?before=" + all[3] + "&after=" + all[2], "before"},
		{"after and around", "?after=" + all[3] + "&around=" + all[2], "around"},
		{"all three", "?before=a&after=b&around=c", "after"},
		{"limit zero", "?limit=0", "limit"},
		{"limit negative", "?limit=-3", "limit"},
		{"limit over max", "?limit=101", "limit"},
		{"limit not a number", "?limit=ten", "limit"},
		{"limit twice", "?limit=5&limit=6", "limit"},
		{"empty anchor", "?before=", "before"},
		{"anchor twice", "?before=" + all[3] + "&before=" + all[4], "before"},
		{"anchor too long", "?before=" + strings.Repeat("A", 65), "before"},
	} {
		rec := admin.do(http.MethodGet, base+tc.query, nil)
		er := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
		if er.FieldErrors()[tc.field] == "" {
			t.Errorf("%s: details = %+v, want fields.%s", tc.name, er.Details, tc.field)
		}
	}
	wantStatus(t, admin.do(http.MethodGet, base+"?around=01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), http.StatusNotFound, CodeNotFound)
}

func TestPaginationThreadReplies(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "threads", protocol.ChannelPublic)
	root := admin.post(t, ch.ID, "root")
	var replies []string
	for i := range 7 {
		replies = append(replies, admin.reply(t, ch.ID, root.ID, "reply "+strconv.Itoa(i)).ID)
	}
	other := admin.post(t, ch.ID, "unrelated")

	// Replies stay out of the channel list, which reports reply_count.
	rec := admin.do(http.MethodGet, "/api/v1/channels/"+ch.ID+"/messages", nil)
	page := decodeBody[protocol.MessagePage](t, rec)
	wantIDs(t, "root messages", page.Items, []string{root.ID, other.ID})
	if page.Items[0].ReplyCount != 7 || page.Items[0].LastReplyAt == nil {
		t.Errorf("root = %+v", page.Items[0])
	}

	thread := func(id, query string) protocol.Thread {
		t.Helper()
		rec := admin.do(http.MethodGet, "/api/v1/messages/"+id+"/thread"+query, nil)
		wantStatus(t, rec, http.StatusOK, "")
		return decodeBody[protocol.Thread](t, rec)
	}
	th := thread(root.ID, "?limit=3")
	if th.Root.ID != root.ID {
		t.Errorf("root = %s, want %s", th.Root.ID, root.ID)
	}
	wantIDs(t, "newest replies", th.Replies, replies[4:])
	if !th.HasMoreBefore || th.HasMoreAfter {
		t.Errorf("flags = %v/%v", th.HasMoreBefore, th.HasMoreAfter)
	}
	th = thread(replies[3], "?limit=3&after="+replies[1])
	wantIDs(t, "after", th.Replies, replies[2:5])
	if th.Root.ID != root.ID || !th.HasMoreBefore || !th.HasMoreAfter {
		t.Errorf("thread via a reply = root %s flags %v/%v", th.Root.ID, th.HasMoreBefore, th.HasMoreAfter)
	}
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/messages/"+root.ID+"/thread?before=a&after=b", nil), http.StatusBadRequest, CodeBadRequest)
}

func TestPaginationParser(t *testing.T) {
	tests := []struct {
		query   string
		want    chat.PageOptions
		wantErr string
	}{
		{"", chat.PageOptions{}, ""},
		{"limit=100", chat.PageOptions{Limit: 100}, ""},
		{"before=abc&limit=7", chat.PageOptions{Before: "abc", Limit: 7}, ""},
		{"after=abc", chat.PageOptions{After: "abc"}, ""},
		{"around=abc", chat.PageOptions{Around: "abc"}, ""},
		{"unknown=1", chat.PageOptions{}, ""},
		{"before=a&after=b", chat.PageOptions{}, "before"},
		{"limit=0", chat.PageOptions{}, "limit"},
		{"limit=1e2", chat.PageOptions{}, "limit"},
		{"limit=", chat.PageOptions{}, "limit"},
		{"around=", chat.PageOptions{}, "around"},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x?"+tc.query, nil)
		got, ok := parsePage(rec, req)
		if tc.wantErr == "" {
			if !ok || got != tc.want {
				t.Errorf("%q: got %+v ok=%v, want %+v", tc.query, got, ok, tc.want)
			}
			continue
		}
		if ok || rec.Code != http.StatusBadRequest {
			t.Errorf("%q: ok=%v status=%d, want 400", tc.query, ok, rec.Code)
			continue
		}
		if er := decodeError(t, rec); er.FieldErrors()[tc.wantErr] == "" {
			t.Errorf("%q: details = %+v, want fields.%s", tc.query, er.Details, tc.wantErr)
		}
	}
}

func TestChatInvisibleChannelsAre404(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "")
	bob := e.newMember("bob", "")
	carol := e.newMember("carol", "")
	gus := e.newGuest("gus", "")

	secret := alice.createChannel(t, "secret", protocol.ChannelPrivate, bob)
	msg := alice.post(t, secret.ID, "classified")
	reply := bob.reply(t, secret.ID, msg.ID, "agreed")
	wantStatus(t, alice.do(http.MethodPut, "/api/v1/channels/"+secret.ID+"/pins/"+msg.ID, nil), http.StatusOK, "")
	wantStatus(t, bob.do(http.MethodPut, "/api/v1/messages/"+msg.ID+"/reactions/"+url.PathEscape("👍"), nil), http.StatusOK, "")

	dm := decodeBody[protocol.Channel](t, alice.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{bob.id}}))
	dmMsg := alice.post(t, dm.ID, "psst")

	type call struct {
		method, path string
		body         any
	}
	callsFor := func(ch protocol.Channel, m protocol.Message, r protocol.Message) []call {
		c := "/api/v1/channels/" + ch.ID
		mp := "/api/v1/messages/" + m.ID
		return []call{
			{http.MethodGet, c + "/messages", nil},
			{http.MethodGet, c + "/messages?around=" + m.ID, nil},
			{http.MethodPost, c + "/messages", protocol.CreateMessageRequest{Body: "let me in"}},
			{http.MethodGet, c + "/members", nil},
			{http.MethodPut, c + "/members/" + carol.id, nil},
			{http.MethodDelete, c + "/members/" + bob.id, nil},
			{http.MethodGet, c + "/pins", nil},
			{http.MethodPut, c + "/pins/" + m.ID, nil},
			{http.MethodDelete, c + "/pins/" + m.ID, nil},
			{http.MethodPatch, c, protocol.UpdateChannelRequest{Topic: ptr("hijack")}},
			{http.MethodPost, c + "/archive", nil},
			{http.MethodPost, c + "/unarchive", nil},
			{http.MethodPost, c + "/leave", nil},
			{http.MethodPatch, c + "/membership", protocol.UpdateMembershipRequest{NotifyLevel: ptr("none")}},
			{http.MethodPost, c + "/read", protocol.MarkReadRequest{MessageID: m.ID}},
			{http.MethodGet, mp + "/thread", nil},
			{http.MethodGet, "/api/v1/messages/" + r.ID + "/thread", nil},
			{http.MethodPatch, mp, protocol.EditMessageRequest{Body: "rewritten"}},
			{http.MethodDelete, mp, nil},
			{http.MethodPut, mp + "/reactions/" + url.PathEscape("👍"), nil},
			{http.MethodDelete, mp + "/reactions/" + url.PathEscape("👍"), nil},
		}
	}
	privateCalls := callsFor(secret, msg, reply)
	dmCalls := callsFor(dm, dmMsg, dmMsg)
	missing := protocol.Channel{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	ghost := protocol.Message{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	ghostCalls := callsFor(missing, ghost, ghost)

	// The fake clock moves on between calls so the session rate limit (20/s)
	// never interferes.
	try := func(c member, call call) *httptest.ResponseRecorder {
		e.clock.Advance(time.Second)
		return c.do(call.method, call.path, call.body)
	}
	for who, c := range map[string]member{"outsider member": carol, "admin who is not a member": admin, "guest": gus} {
		for i, call := range privateCalls {
			rec := try(c, call)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s: %s %s = %d, want 404 (%s)", who, call.method, call.path, rec.Code, rec.Body.String())
				continue
			}
			wantStatus(t, rec, http.StatusNotFound, CodeNotFound)
			// Exactly the answer for a channel or message that does not exist.
			ghostRec := try(c, ghostCalls[i])
			if rec.Body.String() != ghostRec.Body.String() {
				t.Errorf("%s: %s %s body %q differs from a missing resource's %q", who, call.method, call.path, rec.Body.String(), ghostRec.Body.String())
			}
		}
		for _, call := range dmCalls {
			wantStatus(t, try(c, call), http.StatusNotFound, CodeNotFound)
		}
	}

	// Nothing leaked through the writes above.
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE channel_id = ?`, secret.ID); n != 2 {
		t.Errorf("%d messages in the private channel, want 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ?`, secret.ID); n != 2 {
		t.Errorf("%d members of the private channel, want 2", n)
	}

	// Lists never mention what the caller cannot see.
	for who, c := range map[string]member{"carol": carol, "admin": admin} {
		for _, scope := range []string{"joined", "public"} {
			rec := c.do(http.MethodGet, "/api/v1/channels?scope="+scope, nil)
			for _, ch := range decodeBody[protocol.ChannelList](t, rec).Items {
				if ch.ID == secret.ID || ch.ID == dm.ID {
					t.Errorf("%s: scope=%s lists %s", who, scope, ch.ID)
				}
			}
		}
		b := decodeBody[protocol.Bootstrap](t, c.do(http.MethodGet, "/api/v1/bootstrap", nil))
		for _, ch := range b.Channels {
			if ch.ID == secret.ID || ch.ID == dm.ID {
				t.Errorf("%s: bootstrap lists %s", who, ch.ID)
			}
		}
	}

	// The members themselves can use all of it.
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/channels/"+secret.ID+"/messages", nil), http.StatusOK, "")
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/channels/"+dm.ID+"/pins", nil), http.StatusOK, "")
}

func ptr[T any](v T) *T { return &v }

func TestChatGuestCannotBrowseDirectory(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	gus := e.newGuest("gus", "")
	general := e.generalID()
	room := admin.createChannel(t, "guestroom", protocol.ChannelPrivate, gus)

	wantStatus(t, gus.do(http.MethodGet, "/api/v1/channels?scope=public", nil), http.StatusForbidden, CodeForbidden)
	rec := gus.do(http.MethodGet, "/api/v1/channels", nil)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeBody[protocol.ChannelList](t, rec).Items
	if len(list) != 1 || list[0].ID != room.ID {
		t.Errorf("guest channels = %+v, want only the one it was added to", list)
	}

	// A public channel the guest was not added to does not exist for it.
	wantStatus(t, gus.do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, gus.do(http.MethodPost, "/api/v1/channels/"+general+"/join", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, gus.do(http.MethodPost, "/api/v1/channels", protocol.CreateChannelRequest{Name: "mine", Kind: "public"}), http.StatusForbidden, CodeForbidden)

	b := decodeBody[protocol.Bootstrap](t, gus.do(http.MethodGet, "/api/v1/bootstrap", nil))
	if len(b.Channels) != 1 || b.Channels[0].ID != room.ID {
		t.Errorf("guest bootstrap channels = %+v", b.Channels)
	}
	// Members can browse it.
	m := e.newMember("mia", "")
	rec = m.do(http.MethodGet, "/api/v1/channels?scope=public", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.ChannelList](t, rec).Items; len(got) != 1 || got[0].ID != general {
		t.Errorf("member directory = %+v, want #general only", got)
	}
}

func TestChatUsers(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	gus := e.newGuest("gus", "")
	zed := e.newMember("zed", "")
	admin.createChannel(t, "pair", protocol.ChannelPrivate, gus)

	names := func(c member, query string) []string {
		t.Helper()
		rec := c.do(http.MethodGet, "/api/v1/users"+query, nil)
		wantStatus(t, rec, http.StatusOK, "")
		var out []string
		for _, u := range decodeBody[protocol.UserList](t, rec).Items {
			out = append(out, u.Username)
		}
		return out
	}
	if got := names(bob, ""); !slices.Equal(got, []string{"admin", "bob", "gus", "zed"}) {
		t.Errorf("member sees %v", got)
	}
	if got := names(bob, "?q=Z"); !slices.Equal(got, []string{"zed"}) {
		t.Errorf("q=Z -> %v", got)
	}
	if got := names(gus, ""); !slices.Equal(got, []string{"admin", "gus"}) {
		t.Errorf("guest sees %v", got)
	}
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/users?q="+strings.Repeat("a", 65), nil), http.StatusBadRequest, CodeBadRequest)

	rec := bob.do(http.MethodGet, "/api/v1/users/"+zed.id, nil)
	wantStatus(t, rec, http.StatusOK, "")
	if u := decodeBody[protocol.User](t, rec); u.Username != "zed" || u.Role != "member" || u.Kind != "human" {
		t.Errorf("user = %+v", u)
	}
	wantStatus(t, gus.do(http.MethodGet, "/api/v1/users/"+zed.id, nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/users/01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), http.StatusNotFound, CodeNotFound)
}

// TestChatDirectoryNeedsReadScope: the account directory is read by apps only
// with messages:read or channels:read; a token with just alerts:write is 403.
func TestChatDirectoryNeedsReadScope(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ops := admin.createChannel(t, "ops", protocol.ChannelPublic)
	narrow := e.newBot(admin, "narrow", []string{"alerts:write"}, nil, ops.ID)
	wantStatus(t, narrow.do(e, http.MethodGet, "/api/v1/bootstrap", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, narrow.do(e, http.MethodGet, "/api/v1/users", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, narrow.do(e, http.MethodGet, "/api/v1/users/"+admin.id, nil), http.StatusForbidden, CodeForbidden)

	for i, scope := range []string{"messages:read", "channels:read"} {
		reader := e.newBot(admin, fmt.Sprintf("reader%d", i), []string{scope}, nil, ops.ID)
		wantStatus(t, reader.do(e, http.MethodGet, "/api/v1/bootstrap", nil), http.StatusOK, "")
		wantStatus(t, reader.do(e, http.MethodGet, "/api/v1/users", nil), http.StatusOK, "")
		wantStatus(t, reader.do(e, http.MethodGet, "/api/v1/users/"+admin.id, nil), http.StatusOK, "")
	}
}

func TestChatChannelLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	owner := e.newMember("olivia", "")
	bob := e.newMember("bob", "")
	carol := e.newMember("carol", "")

	ch := owner.createChannel(t, " #Project-X ", protocol.ChannelPublic)
	if ch.Name == nil || *ch.Name != "project-x" || ch.Kind != protocol.ChannelPublic || ch.MemberCount != 1 {
		t.Errorf("channel = %+v", ch)
	}
	// Names are unique; bad input is a field error.
	wantStatus(t, owner.do(http.MethodPost, "/api/v1/channels", protocol.CreateChannelRequest{Name: "project-x", Kind: "public"}), http.StatusConflict, CodeConflict)
	er := wantStatus(t, owner.do(http.MethodPost, "/api/v1/channels", protocol.CreateChannelRequest{Name: "ok", Kind: "dm"}), http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["kind"] == "" {
		t.Errorf("details = %+v, want fields.kind", er.Details)
	}
	wantStatus(t, owner.do(http.MethodPost, "/api/v1/channels", protocol.CreateChannelRequest{Name: "", Kind: "public"}), http.StatusBadRequest, CodeBadRequest)

	// Join and leave.
	rec := bob.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/join", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if m := decodeBody[protocol.Membership](t, rec); m.UserID != bob.id || m.ChannelID != ch.ID || m.Role != protocol.MemberMember || m.NotifyLevel == "" {
		t.Errorf("membership = %+v", m)
	}
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/join", nil), http.StatusOK, "")
	rec = bob.do(http.MethodGet, "/api/v1/channels/"+ch.ID+"/members", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.MemberList](t, rec).Items; len(got) != 2 || got[0].UserID != owner.id || got[0].Role != protocol.MemberOwner {
		t.Errorf("members = %+v", got)
	}
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/leave", nil), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/leave", nil), http.StatusForbidden, CodeForbidden)

	// Only the owner manages; others see it but are refused.
	wantStatus(t, bob.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, protocol.UpdateChannelRequest{Topic: ptr("nope")}), http.StatusForbidden, CodeForbidden)
	rec = owner.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, protocol.UpdateChannelRequest{Topic: ptr("launch"), Description: ptr("the launch")})
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.Topic != "launch" || got.Description != "the launch" || got.RetentionDays != nil {
		t.Errorf("updated = %+v", got)
	}
	rec = owner.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, `{"retention_days": 30}`)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.RetentionDays == nil || *got.RetentionDays != 30 || got.Topic != "launch" {
		t.Errorf("retention set: %+v", got)
	}
	rec = owner.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, `{"topic": "later"}`)
	if got := decodeBody[protocol.Channel](t, rec); got.RetentionDays == nil || *got.RetentionDays != 30 {
		t.Errorf("retention changed by an unrelated patch: %+v", got)
	}
	rec = owner.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, `{"retention_days": null}`)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.RetentionDays != nil {
		t.Errorf("retention not cleared: %+v", got)
	}
	wantStatus(t, owner.do(http.MethodPatch, "/api/v1/channels/"+ch.ID, `{"retention_days": "soon"}`), http.StatusBadRequest, CodeBadRequest)

	// Members of a private channel: owner adds, role body is optional.
	priv := owner.createChannel(t, "inner", protocol.ChannelPrivate)
	pm := "/api/v1/channels/" + priv.ID + "/members/"
	rec = owner.do(http.MethodPut, pm+bob.id, nil)
	wantStatus(t, rec, http.StatusOK, "")
	if m := decodeBody[protocol.Membership](t, rec); m.UserID != bob.id || m.Role != protocol.MemberMember {
		t.Errorf("added member = %+v", m)
	}
	rec = owner.do(http.MethodPut, pm+bob.id, protocol.AddMemberRequest{Role: protocol.MemberOwner})
	wantStatus(t, rec, http.StatusOK, "")
	if m := decodeBody[protocol.Membership](t, rec); m.Role != protocol.MemberOwner {
		t.Errorf("promoted member = %+v", m)
	}
	wantStatus(t, owner.do(http.MethodPut, pm+bob.id, protocol.AddMemberRequest{Role: "boss"}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, owner.do(http.MethodPut, pm+"01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, carol.do(http.MethodPut, pm+carol.id, nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, owner.do(http.MethodDelete, pm+bob.id, nil), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/channels/"+priv.ID+"/members", nil), http.StatusNotFound, CodeNotFound)

	// Archive makes the channel read-only; unarchive restores it.
	rec = owner.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/archive", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.ArchivedAt == nil {
		t.Errorf("archived_at not set: %+v", got)
	}
	wantStatus(t, owner.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/messages", protocol.CreateMessageRequest{Body: "late"}), http.StatusConflict, CodeConflict)
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/archive", nil), http.StatusForbidden, CodeForbidden)
	rec = owner.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/unarchive", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.ArchivedAt != nil {
		t.Errorf("archived_at still set: %+v", got)
	}
	wantStatus(t, owner.do(http.MethodPost, "/api/v1/channels/"+ch.ID+"/messages", protocol.CreateMessageRequest{Body: "back"}), http.StatusCreated, "")

	// The admin lists the directory; scope is validated.
	rec = admin.do(http.MethodGet, "/api/v1/channels?scope=public", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if n := len(decodeBody[protocol.ChannelList](t, rec).Items); n != 2 {
		t.Errorf("public directory has %d channels, want 2", n)
	}
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/channels?scope=everything", nil), http.StatusBadRequest, CodeBadRequest)
}

func TestChatMembershipSettingsAndReadState(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	general := e.generalID()
	first := admin.post(t, general, "one")
	second := admin.post(t, general, "two @bob")

	unread := func() protocol.Unread {
		t.Helper()
		b := decodeBody[protocol.Bootstrap](t, bob.do(http.MethodGet, "/api/v1/bootstrap", nil))
		for _, u := range b.Unread {
			if u.ChannelID == general {
				return u
			}
		}
		return protocol.Unread{}
	}
	if u := unread(); u.Unread != 2 || u.Mentions != 1 {
		t.Errorf("unread before = %+v", u)
	}
	rec := bob.do(http.MethodPost, "/api/v1/channels/"+general+"/read", protocol.MarkReadRequest{MessageID: first.ID})
	wantStatus(t, rec, http.StatusOK, "")
	if st := decodeBody[protocol.ReadEvent](t, rec); st.ChannelID != general || st.LastReadID != first.ID || st.Unread != 1 || st.Mentions != 1 {
		t.Errorf("read state = %+v", st)
	}
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+general+"/read", protocol.MarkReadRequest{MessageID: second.ID}), http.StatusOK, "")
	if u := unread(); u.Unread != 0 || u.Mentions != 0 {
		t.Errorf("unread after = %+v", u)
	}
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+general+"/read", `{}`), http.StatusBadRequest, CodeBadRequest)
	er400 := wantStatus(t, bob.do(http.MethodPost, "/api/v1/channels/"+general+"/read", protocol.MarkReadRequest{MessageID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}), http.StatusBadRequest, CodeBadRequest)
	if er400.FieldErrors()["message_id"] == "" {
		t.Errorf("details = %+v, want fields.message_id", er400.Details)
	}

	// Notification settings.
	path := "/api/v1/channels/" + general + "/membership"
	rec = bob.do(http.MethodPatch, path, protocol.UpdateMembershipRequest{NotifyLevel: ptr("mentions"), AlertMinSeverity: ptr("critical")})
	wantStatus(t, rec, http.StatusOK, "")
	m := decodeBody[protocol.Membership](t, rec)
	if m.NotifyLevel != "mentions" || m.AlertMinSeverity != "critical" || m.MutedUntil != nil {
		t.Errorf("membership = %+v", m)
	}
	until := "2030-01-02T03:04:05Z"
	rec = bob.do(http.MethodPatch, path, protocol.UpdateMembershipRequest{MutedUntil: protocol.Patch[string]{Set: true, Value: &until}})
	wantStatus(t, rec, http.StatusOK, "")
	m = decodeBody[protocol.Membership](t, rec)
	if m.MutedUntil == nil || *m.MutedUntil != "2030-01-02T03:04:05.000Z" || m.NotifyLevel != "mentions" {
		t.Errorf("muted membership = %+v", m)
	}
	rec = bob.do(http.MethodPatch, path, `{"muted_until": null}`)
	wantStatus(t, rec, http.StatusOK, "")
	if m = decodeBody[protocol.Membership](t, rec); m.MutedUntil != nil {
		t.Errorf("still muted: %+v", m)
	}
	er := wantStatus(t, bob.do(http.MethodPatch, path, `{"muted_until": "tomorrow"}`), http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["muted_until"] == "" {
		t.Errorf("details = %+v, want fields.muted_until", er.Details)
	}
	wantStatus(t, bob.do(http.MethodPatch, path, protocol.UpdateMembershipRequest{NotifyLevel: ptr("loud")}), http.StatusBadRequest, CodeBadRequest)
}

func TestChatDirectMessages(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	bob := e.newMember("bob", "")
	carol := e.newMember("carol", "")
	dave := e.newMember("dave", "")

	rec := admin.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{bob.id}})
	wantStatus(t, rec, http.StatusCreated, "")
	dm := decodeBody[protocol.Channel](t, rec)
	if dm.Kind != protocol.ChannelDM || dm.Name != nil || len(dm.ParticipantIDs) != 2 {
		t.Errorf("dm = %+v", dm)
	}
	// Get-or-create: from either side, same channel, 200.
	rec = bob.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{admin.id}})
	wantStatus(t, rec, http.StatusOK, "")
	if again := decodeBody[protocol.Channel](t, rec); again.ID != dm.ID {
		t.Errorf("second start = %s, want %s", again.ID, dm.ID)
	}
	rec = admin.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{carol.id, bob.id}})
	wantStatus(t, rec, http.StatusCreated, "")
	group := decodeBody[protocol.Channel](t, rec)
	if group.Kind != protocol.ChannelGroupDM || len(group.ParticipantIDs) != 3 {
		t.Errorf("group dm = %+v", group)
	}
	rec = carol.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{bob.id, admin.id}})
	wantStatus(t, rec, http.StatusOK, "")
	if again := decodeBody[protocol.Channel](t, rec); again.ID != group.ID {
		t.Errorf("group restart = %s, want %s", again.ID, group.ID)
	}

	// Messages flow; outsiders cannot see them.
	bob.post(t, dm.ID, "hi admin")
	wantStatus(t, dave.do(http.MethodGet, "/api/v1/channels/"+dm.ID+"/messages", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, carol.do(http.MethodGet, "/api/v1/channels/"+dm.ID+"/messages", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/channels/"+dm.ID+"/join", nil), http.StatusForbidden, CodeForbidden)

	// Bad requests.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/dms", `{}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/dms", `{"user_ids": null}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{"01ARZ3NDEKTSV4RRFFQ69G5FAV"}}), http.StatusBadRequest, CodeBadRequest)
	// Note to self.
	rec = admin.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{}})
	wantStatus(t, rec, http.StatusCreated, "")
}

func TestChatEditDeleteAndAdminModeration(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "")
	bob := e.newMember("bob", "")
	general := e.generalID()

	msg := alice.post(t, general, "draft")
	rec := alice.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: "final"})
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Message](t, rec); got.Body != "final" || got.EditedAt == nil {
		t.Errorf("edited = %+v", got)
	}
	// Nobody else edits it, not even an admin.
	wantStatus(t, bob.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: "mine now"}), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: "mine now"}), http.StatusForbidden, CodeForbidden)
	wantStatus(t, alice.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: " "}), http.StatusBadRequest, CodeBadRequest)

	// A plain member cannot delete someone else's message; the permission
	// decision belongs to the chat service.
	wantStatus(t, bob.do(http.MethodDelete, "/api/v1/messages/"+msg.ID, nil), http.StatusForbidden, CodeForbidden)
	// The admin moderates a public channel.
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/messages/"+msg.ID, nil), http.StatusNoContent, "")
	rec = alice.do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil)
	items := decodeBody[protocol.MessagePage](t, rec).Items
	if len(items) != 1 || !items[0].Deleted || items[0].Body != "" {
		t.Errorf("after moderation delete: %+v", items)
	}
	wantStatus(t, alice.do(http.MethodPatch, "/api/v1/messages/"+msg.ID, protocol.EditMessageRequest{Body: "revive"}), http.StatusConflict, CodeConflict)
	// Deleting again is a no-op for the admin.
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/messages/"+msg.ID, nil), http.StatusNoContent, "")

	// An author deletes their own message.
	own := bob.post(t, general, "oops")
	wantStatus(t, bob.do(http.MethodDelete, "/api/v1/messages/"+own.ID, nil), http.StatusNoContent, "")

	// The admin cannot moderate a private channel they are not in.
	secret := alice.createChannel(t, "private-talk", protocol.ChannelPrivate)
	hidden := alice.post(t, secret.ID, "secret")
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/messages/"+hidden.ID, nil), http.StatusNotFound, CodeNotFound)
	// A channel owner moderates their channel.
	wantStatus(t, alice.do(http.MethodPut, "/api/v1/channels/"+secret.ID+"/members/"+bob.id, nil), http.StatusOK, "")
	bobs := bob.post(t, secret.ID, "bob speaks")
	wantStatus(t, alice.do(http.MethodDelete, "/api/v1/messages/"+bobs.ID, nil), http.StatusNoContent, "")
}

func TestChatPinsAndThread(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "")
	general := e.generalID()
	root := alice.post(t, general, "pin me")
	other := bob.post(t, general, "and me")
	base := "/api/v1/channels/" + general + "/pins/"

	rec := bob.do(http.MethodPut, base+root.ID, nil)
	wantStatus(t, rec, http.StatusOK, "")
	if p := decodeBody[protocol.Pin](t, rec); p.Message.ID != root.ID || p.PinnedBy == nil || *p.PinnedBy != bob.id {
		t.Errorf("pin = %+v", p)
	}
	wantStatus(t, alice.do(http.MethodPut, base+root.ID, nil), http.StatusOK, "")
	e.clock.Advance(time.Second)
	wantStatus(t, alice.do(http.MethodPut, base+other.ID, nil), http.StatusOK, "")
	rec = alice.do(http.MethodGet, "/api/v1/channels/"+general+"/pins", nil)
	pins := decodeBody[protocol.PinList](t, rec).Items
	if len(pins) != 2 || pins[0].Message.ID != other.ID || pins[1].Message.ID != root.ID {
		t.Errorf("pins = %+v, want newest first", pins)
	}
	wantStatus(t, alice.do(http.MethodPut, base+"01ARZ3NDEKTSV4RRFFQ69G5FAV", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, bob.do(http.MethodDelete, base+root.ID, nil), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodDelete, base+root.ID, nil), http.StatusNoContent, "")
	rec = alice.do(http.MethodGet, "/api/v1/channels/"+general+"/pins", nil)
	if pins = decodeBody[protocol.PinList](t, rec).Items; len(pins) != 1 {
		t.Errorf("pins after unpin = %d, want 1", len(pins))
	}

	// Threads: replies, then the thread read through a reply id.
	r1 := bob.reply(t, general, root.ID, "first reply")
	nested := alice.reply(t, general, r1.ID, "reply to a reply")
	if nested.ThreadRootID == nil || *nested.ThreadRootID != root.ID {
		t.Errorf("a reply to a reply belongs to thread %v, want %s", nested.ThreadRootID, root.ID)
	}
	rec = alice.do(http.MethodGet, "/api/v1/messages/"+r1.ID+"/thread", nil)
	wantStatus(t, rec, http.StatusOK, "")
	th := decodeBody[protocol.Thread](t, rec)
	wantIDs(t, "replies", th.Replies, []string{r1.ID, nested.ID})
	if th.Root.ID != root.ID || th.Root.ReplyCount != 2 {
		t.Errorf("thread root = %+v", th.Root)
	}
}

// emojiCases are single reactions made of several code points.
var emojiCases = []struct{ name, emoji string }{
	{"thumbs up", "👍"},
	{"skin tone", "👍🏽"},
	{"zwj family", "👨‍👩‍👧‍👦"},
	{"zwj rainbow flag", "🏳️‍🌈"},
	{"zwj profession with skin tone", "👩🏿‍🚀"},
	{"flag", "🇯🇵"},
	{"subdivision flag", "🏴󠁧󠁢󠁥󠁮󠁧󠁿"},
	{"keycap", "1️⃣"},
	{"variation selector", "❤️"},
}

func TestChatReactionsEmojiRoundTrip(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	bob := e.newMember("bob", "")
	general := e.generalID()
	msg := alice.post(t, general, "react to me")

	for _, tc := range emojiCases {
		path := "/api/v1/messages/" + msg.ID + "/reactions/" + url.PathEscape(tc.emoji)
		rec := bob.do(http.MethodPut, path, nil)
		wantStatus(t, rec, http.StatusOK, "")
		var found *protocol.Reaction
		list := decodeBody[protocol.ReactionList](t, rec).Items
		for i := range list {
			if list[i].Emoji == tc.emoji {
				found = &list[i]
			}
		}
		if found == nil || found.Count != 1 || !slices.Equal(found.UserIDs, []string{bob.id}) {
			t.Errorf("%s: PUT answered %+v", tc.name, list)
		}
		// A second PUT changes nothing; a second user adds to the count.
		wantStatus(t, bob.do(http.MethodPut, path, nil), http.StatusOK, "")
		rec = alice.do(http.MethodPut, path, nil)
		for _, r := range decodeBody[protocol.ReactionList](t, rec).Items {
			if r.Emoji == tc.emoji && r.Count != 2 {
				t.Errorf("%s: count = %d, want 2", tc.name, r.Count)
			}
		}
	}

	// Stored byte for byte, and listed with the message.
	rec := alice.do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil)
	got := map[string]int{}
	for _, r := range decodeBody[protocol.MessagePage](t, rec).Items[0].Reactions {
		got[r.Emoji] = r.Count
	}
	if len(got) != len(emojiCases) {
		t.Errorf("message has %d distinct reactions, want %d: %v", len(got), len(emojiCases), got)
	}
	for _, tc := range emojiCases {
		if got[tc.emoji] != 2 {
			t.Errorf("%s: message lists %d reactions with the exact emoji, want 2", tc.name, got[tc.emoji])
		}
		var stored int
		if err := e.db.Read().QueryRow(`SELECT COUNT(*) FROM reactions WHERE message_id = ? AND emoji = ?`, msg.ID, tc.emoji).Scan(&stored); err != nil || stored != 2 {
			t.Errorf("%s: %d stored rows with the exact bytes (%v), want 2", tc.name, stored, err)
		}
	}

	// DELETE removes only the caller's reaction, again by exact emoji.
	for _, tc := range emojiCases {
		path := "/api/v1/messages/" + msg.ID + "/reactions/" + url.PathEscape(tc.emoji)
		rec := bob.do(http.MethodDelete, path, nil)
		wantStatus(t, rec, http.StatusOK, "")
		for _, r := range decodeBody[protocol.ReactionList](t, rec).Items {
			if r.Emoji == tc.emoji && (r.Count != 1 || !slices.Equal(r.UserIDs, []string{alice.id})) {
				t.Errorf("%s: after DELETE %+v", tc.name, r)
			}
		}
		wantStatus(t, bob.do(http.MethodDelete, path, nil), http.StatusOK, "")
	}
	for _, tc := range emojiCases {
		path := "/api/v1/messages/" + msg.ID + "/reactions/" + url.PathEscape(tc.emoji)
		rec := alice.do(http.MethodDelete, path, nil)
		wantStatus(t, rec, http.StatusOK, "")
	}
	rec = alice.do(http.MethodGet, "/api/v1/channels/"+general+"/messages", nil)
	if rs := decodeBody[protocol.MessagePage](t, rec).Items[0].Reactions; len(rs) != 0 {
		t.Errorf("reactions left: %+v", rs)
	}
}

func TestChatReactionsRejectBadEmoji(t *testing.T) {
	e := newEnv(t)
	alice := e.newAdmin()
	general := e.generalID()
	msg := alice.post(t, general, "react to me")
	base := "/api/v1/messages/" + msg.ID + "/reactions/"

	er := wantStatus(t, alice.do(http.MethodPut, base+url.PathEscape(strings.Repeat("😀", 17)), nil), http.StatusBadRequest, CodeBadRequest)
	if er.FieldErrors()["emoji"] == "" {
		t.Errorf("details = %+v, want fields.emoji", er.Details)
	}
	wantStatus(t, alice.do(http.MethodPut, base+"%20", nil), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, alice.do(http.MethodPut, "/api/v1/messages/nope/reactions/"+url.PathEscape("👍"), nil), http.StatusNotFound, CodeNotFound)
	if n := e.count(`SELECT COUNT(*) FROM reactions`); n != 0 {
		t.Errorf("%d reactions stored by rejected requests", n)
	}
}

func TestChatErrorMapping(t *testing.T) {
	a := &api{logger: slog.New(slog.DiscardHandler)}
	secret := "dial tcp 10.1.2.3:5432: password=hunter2"
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		wantMsg    string
		wantFields map[string]string
	}{
		{"not found", chat.ErrNotFound, 404, CodeNotFound, "not found", nil},
		{"typed not found hides its text", &chat.Error{Kind: chat.ErrNotFound, Msg: "channel secret exists"}, 404, CodeNotFound, "not found", nil},
		{"wrapped not found", fmt.Errorf("load: %w", chat.ErrNotFound), 404, CodeNotFound, "not found", nil},
		{"forbidden", &chat.Error{Kind: chat.ErrForbidden, Msg: "join the channel first"}, 403, CodeForbidden, "join the channel first", nil},
		{"bare forbidden", chat.ErrForbidden, 403, CodeForbidden, "you are not allowed to do that", nil},
		{"conflict", &chat.Error{Kind: chat.ErrConflict, Msg: "the channel is archived"}, 409, CodeConflict, "the channel is archived", nil},
		{"validation", &chat.ValidationError{Fields: map[string]string{"body": "must not be empty"}}, 400, CodeBadRequest, "one or more fields are invalid", map[string]string{"body": "must not be empty"}},
		{"wrapped validation", fmt.Errorf("x: %w", &chat.ValidationError{Fields: map[string]string{"nonce": "reused"}}), 400, CodeBadRequest, "one or more fields are invalid", map[string]string{"nonce": "reused"}},
		{"writer exited", store.ErrFnExited, 500, CodeInternal, "internal server error", nil},
		{"unknown", errors.New(secret), 500, CodeInternal, "internal server error", nil},
		{"closed store", store.ErrClosed, 503, CodeUnavailable, "the server is busy or shutting down; try again", nil},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		a.fail(rec, httptest.NewRequest(http.MethodGet, "/x", nil), tc.err)
		if rec.Code != tc.status {
			t.Errorf("%s: status = %d, want %d", tc.name, rec.Code, tc.status)
			continue
		}
		er := decodeError(t, rec)
		if er.Code != tc.code || er.Message != tc.wantMsg {
			t.Errorf("%s: error = %+v", tc.name, er)
		}
		if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "secret exists") {
			t.Errorf("%s: body leaks internals: %s", tc.name, rec.Body.String())
		}
		for k, v := range tc.wantFields {
			if er.FieldErrors()[k] != v {
				t.Errorf("%s: fields = %v, want %s=%s", tc.name, er.FieldErrors(), k, v)
			}
		}
	}
}

func TestChatActorFromPrincipal(t *testing.T) {
	tests := []struct {
		user auth.User
		want chat.Actor
	}{
		{auth.User{ID: "u1", Role: auth.RoleAdmin, Kind: auth.KindHuman}, chat.Actor{ID: "u1", Role: "admin", Kind: "human"}},
		{auth.User{ID: "u2", Role: auth.RoleGuest, Kind: auth.KindHuman}, chat.Actor{ID: "u2", Role: "guest", Kind: "human"}},
		{auth.User{ID: "u3", Role: auth.RoleMember, Kind: auth.KindApp}, chat.Actor{ID: "u3", Role: "member", Kind: "app"}},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(auth.WithPrincipal(req.Context(), &auth.Principal{User: &tc.user, Via: auth.ViaCookie}))
		got := actorFrom(req)
		if got.ID != tc.want.ID || got.Role != tc.want.Role || got.Kind != tc.want.Kind || got.Scopes != nil || got.ChannelIDs != nil {
			t.Errorf("actorFrom(%+v) = %+v, want %+v", tc.user, got, tc.want)
		}
	}
}

// schemaAccepts marshals v and validates it against the named component schema.
func schemaAccepts(t *testing.T, name string, v any, mutate func(map[string]any)) error {
	t.Helper()
	doc, _ := loadSpec(t)
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(m)
	}
	return doc.Components.Schemas[name].Value.VisitJSON(m)
}

// TestContractMessageSchemaMatchesProtocol covers the parts of Message the
// handler tests cannot reach yet (files, alerts) and proves the schemas are
// strict in both directions: a missing or an extra field fails.
func TestContractMessageSchemaMatchesProtocol(t *testing.T) {
	ts := "2026-10-11T18:04:05.123Z"
	thumb := "/files/01ARZ3NDEKTSV4RRFFQ69G5FAV/t/a.png"
	w, h := 640, 480
	user := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	link := "https://example.com/x"
	root := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	msg := protocol.Message{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", ChannelID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", ThreadRootID: &root, Kind: protocol.MessageAlert,
		Author: protocol.Author{ID: user, Username: "netdiag", DisplayName: "netdiag", Kind: protocol.KindApp},
		Body:   "disk full",
		Files: []protocol.File{{
			ID: "01ARZ3NDEKTSV4RRFFQ69G5FAZ", Name: "a.png", Size: 1234, Mime: "image/png", Category: protocol.FileImage,
			Width: &w, Height: &h, HasThumb: true, MetaStripped: true, URL: "/files/x/a.png", ThumbURL: &thumb,
		}},
		Reactions: []protocol.Reaction{{Emoji: "👍🏽", Count: 1, UserIDs: []string{user}}},
		Mentions:  protocol.Mentions{UserIDs: []string{user}, Channel: true},
		CreatedAt: ts,
		Alert: &protocol.Alert{
			Severity: protocol.SeverityCritical, State: protocol.AlertResolved, Title: "Disk", DedupeKey: "disk:/",
			Fields:      []protocol.AlertField{{Name: "IP", Value: "10.0.0.5", Short: true}},
			Link:        &link,
			Occurrences: 3, FirstSeenAt: ts, LastSeenAt: ts,
			AckedBy: &user, AckedAt: &ts, ResolvedBy: &user, ResolvedAt: &ts,
		},
	}
	if err := schemaAccepts(t, "Message", msg, nil); err != nil {
		t.Fatalf("full message rejected: %v", err)
	}
	if err := schemaAccepts(t, "Thread", protocol.Thread{Root: msg, Replies: []protocol.Message{msg}}, nil); err != nil {
		t.Errorf("thread rejected: %v", err)
	}
	if err := schemaAccepts(t, "Pin", protocol.Pin{Message: msg, PinnedBy: &user, PinnedAt: ts}, nil); err != nil {
		t.Errorf("pin rejected: %v", err)
	}
	if err := schemaAccepts(t, "Message", msg, func(m map[string]any) { m["surprise"] = 1 }); err == nil {
		t.Error("an extra field was accepted")
	}
	if err := schemaAccepts(t, "Message", msg, func(m map[string]any) { delete(m, "edited_at") }); err == nil {
		t.Error("a missing field was accepted")
	}
	if err := schemaAccepts(t, "Message", msg, func(m map[string]any) { m["files"] = nil }); err == nil {
		t.Error("files: null was accepted")
	}
	if err := schemaAccepts(t, "Message", msg, func(m map[string]any) { m["kind"] = "poll" }); err == nil {
		t.Error("an unknown kind was accepted")
	}
}
