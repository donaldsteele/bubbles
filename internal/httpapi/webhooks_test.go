package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// hookFixture is an admin, a member, a channel #ops with both and a webhook
// into #ops.
type hookFixture struct {
	admin, alice member
	ops          protocol.Channel
	wh           protocol.CreatedWebhook
	token        string
	botUserID    string
}

func (e *env) newWebhook(admin member, channelID, name string) (protocol.CreatedWebhook, string) {
	e.t.Helper()
	rec := admin.do(http.MethodPost, "/api/v1/admin/webhooks", protocol.CreateWebhookRequest{ChannelID: channelID, Name: name})
	wantStatus(e.t, rec, http.StatusCreated, "")
	wh := decodeBody[protocol.CreatedWebhook](e.t, rec)
	return wh, wh.URL[strings.LastIndex(wh.URL, "/")+1:]
}

func (e *env) hookFixture() hookFixture {
	e.t.Helper()
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	ops := admin.createChannel(e.t, "ops", "public", alice)
	wh, token := e.newWebhook(admin, ops.ID, "Deploy Hook")
	app, err := e.apps.GetApp(e.ctx, wh.AppID)
	if err != nil {
		e.t.Fatal(err)
	}
	return hookFixture{admin: admin, alice: alice, ops: ops, wh: wh, token: token, botUserID: app.UserID}
}

// hook posts to a webhook the way a sender does: from a LAN address, with no
// cookie and no Origin header.
func (e *env) hook(token, contentType, body string, mods ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	all := append([]func(*http.Request){func(r *http.Request) {
		r.Header.Del("Origin")
		if contentType == "" {
			r.Header.Del("Content-Type")
		} else {
			r.Header.Set("Content-Type", contentType)
		}
	}}, mods...)
	return e.client("192.168.1.70").do(http.MethodPost, "/hooks/"+token, body, all...)
}

func (e *env) hookJSON(token, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.hook(token, "application/json", body)
}

// wantHookOK asserts Slack's answer: 200 and the plain text ok.
func wantHookOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("hook answered %d %q (%s), want 200 ok text/plain", rec.Code, rec.Body.String(), rec.Header().Get("Content-Type"))
	}
}

func (f hookFixture) messages(t *testing.T) []protocol.Message {
	t.Helper()
	rec := f.alice.do(http.MethodGet, "/api/v1/channels/"+f.ops.ID+"/messages", nil)
	wantStatus(t, rec, http.StatusOK, "")
	return decodeBody[protocol.MessagePage](t, rec).Items
}

// lastMessage is the newest message of #ops.
func (f hookFixture) lastMessage(t *testing.T) protocol.Message {
	t.Helper()
	items := f.messages(t)
	if len(items) == 0 {
		t.Fatal("no messages in #ops")
	}
	return items[len(items)-1]
}

func (e *env) mentionState(messageID string) (channel, here, rows int) {
	e.t.Helper()
	if err := e.db.Read().QueryRow(`SELECT mention_channel, mention_here FROM messages WHERE id = ?`, messageID).Scan(&channel, &here); err != nil {
		e.t.Fatal(err)
	}
	return channel, here, e.count(`SELECT COUNT(*) FROM mentions WHERE message_id = ?`, messageID)
}

func TestWebhookAdminCreateListRevoke(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()

	// The URL is the server's first LAN host and port plus the secret path.
	if !strings.HasPrefix(f.wh.URL, "https://192.168.1.5:28225/hooks/bwh_") || len(f.token) != len("bwh_")+52 {
		t.Fatalf("url = %q", f.wh.URL)
	}
	if f.wh.ChannelID != f.ops.ID || f.wh.Name != "Deploy Hook" || f.wh.CreatedBy == nil || *f.wh.CreatedBy != f.admin.id || f.wh.RevokedAt != nil {
		t.Fatalf("webhook = %+v", f.wh)
	}

	// A pseudo-app with a bot account exists and is a member of the channel.
	app, err := e.apps.GetApp(e.ctx, f.wh.AppID)
	if err != nil {
		t.Fatal(err)
	}
	if app.Username != "deploy-hook" || app.Name != "Deploy Hook" || app.DisabledAt != nil || len(app.Tokens) != 0 {
		t.Errorf("pseudo-app = %+v", app)
	}
	if n := e.count(`SELECT COUNT(*) FROM users WHERE id = ? AND kind = 'app' AND pw_hash IS NULL`, app.UserID); n != 1 {
		t.Error("the bot account is not a password-less app user")
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, f.ops.ID, app.UserID); n != 1 {
		t.Error("the bot is not a member of the channel")
	}

	// Only the SHA-256 of the secret is stored, and it is nowhere else.
	if n := e.count(`SELECT COUNT(*) FROM webhooks WHERE id = ? AND token_hash = ?`, f.wh.ID, auth.HashToken(f.token)); n != 1 {
		t.Error("the token hash is not stored")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM webhooks WHERE name LIKE ? OR CAST(token_hash AS TEXT) LIKE ?`,
		`SELECT COUNT(*) FROM audit_log WHERE detail LIKE ? OR target_id LIKE ?`,
		`SELECT COUNT(*) FROM events WHERE payload LIKE ? OR type LIKE ?`,
	} {
		like := "%" + f.token[4:] + "%"
		if n := e.count(q, like, like); n != 0 {
			t.Errorf("the token secret is in the database (%s)", q)
		}
	}

	// The list never shows it.
	rec := f.admin.do(http.MethodGet, "/api/v1/admin/webhooks", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), f.token[4:]) || strings.Contains(rec.Body.String(), "/hooks/") {
		t.Error("the secret URL is in the list")
	}
	list := decodeBody[protocol.WebhookList](t, rec)
	if len(list.Items) != 1 || list.Items[0].ID != f.wh.ID || list.Items[0].RevokedAt != nil {
		t.Fatalf("list = %+v", list)
	}

	// Revoking ends the URL and disables the pseudo-app; repeating is harmless.
	wantHookOK(t, e.hookJSON(f.token, `{"text":"before"}`))
	wantStatus(t, f.admin.do(http.MethodDelete, "/api/v1/admin/webhooks/"+f.wh.ID, nil), http.StatusNoContent, "")
	wantStatus(t, e.hookJSON(f.token, `{"text":"after"}`), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.admin.do(http.MethodDelete, "/api/v1/admin/webhooks/"+f.wh.ID, nil), http.StatusNoContent, "")
	wantStatus(t, f.admin.do(http.MethodDelete, "/api/v1/admin/webhooks/nope", nil), http.StatusNotFound, CodeNotFound)
	if app, err = e.apps.GetApp(e.ctx, f.wh.AppID); err != nil || app.DisabledAt == nil {
		t.Errorf("pseudo-app after revoke = %+v, %v", app, err)
	}
	rec = f.admin.do(http.MethodGet, "/api/v1/admin/webhooks", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if list = decodeBody[protocol.WebhookList](t, rec); len(list.Items) != 1 || list.Items[0].RevokedAt == nil {
		t.Errorf("list after revoke = %+v", list)
	}
	if msgs := f.messages(t); len(msgs) != 1 || msgs[0].Body != "before" {
		t.Errorf("messages = %+v", msgs)
	}

	// Create and revoke are audited once each, by the admin.
	for action, want := range map[string]int{"webhook.create": 1, "webhook.revoke": 1, "app.create": 1} {
		if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND actor_kind = 'user' AND actor_id = ?`, action, f.admin.id); n != want {
			t.Errorf("%s audit entries = %d, want %d", action, n, want)
		}
	}
}

func TestWebhookAdminEndpointsAreAdminOnlyAndValidate(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	anon := e.client("")
	b := e.newBot(f.admin, "bot", protocol.AppScopes, nil)
	dm := f.alice.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{f.admin.id}})
	wantStatus(t, dm, http.StatusCreated, "")
	dmChannel := decodeBody[protocol.Channel](t, dm)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/admin/webhooks", nil},
		{http.MethodPost, "/admin/webhooks", protocol.CreateWebhookRequest{ChannelID: f.ops.ID, Name: "x"}},
		{http.MethodDelete, "/admin/webhooks/" + f.wh.ID, nil},
	} {
		wantStatus(t, anon.do(tc.method, "/api/v1"+tc.path, tc.body), http.StatusUnauthorized, CodeUnauthorized)
		wantStatus(t, f.alice.do(tc.method, "/api/v1"+tc.path, tc.body), http.StatusForbidden, CodeForbidden)
		wantStatus(t, b.do(e, tc.method, "/api/v1"+tc.path, tc.body), http.StatusForbidden, CodeForbidden)
		// A cross-origin change with the admin's cookie is refused too.
		if tc.method != http.MethodGet {
			wantStatus(t, f.admin.do(tc.method, "/api/v1"+tc.path, tc.body, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }),
				http.StatusForbidden, CodeForbidden)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM webhooks WHERE revoked_at IS NULL`); n != 1 {
		t.Errorf("live webhooks = %d", n)
	}

	for name, req := range map[string]protocol.CreateWebhookRequest{
		"channel_id": {ChannelID: "nope", Name: "x"},
		"dm":         {ChannelID: dmChannel.ID, Name: "x"},
		"empty":      {Name: "x"},
	} {
		ed := wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/admin/webhooks", req), http.StatusBadRequest, CodeBadRequest)
		if ed.FieldErrors()["channel_id"] == "" {
			t.Errorf("%s: no reason for channel_id: %+v", name, ed)
		}
	}
	for name, n := range map[string]string{"blank": "  ", "long": strings.Repeat("n", 65)} {
		ed := wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/admin/webhooks", protocol.CreateWebhookRequest{ChannelID: f.ops.ID, Name: n}), http.StatusBadRequest, CodeBadRequest)
		if ed.FieldErrors()["name"] == "" {
			t.Errorf("%s name: no reason: %+v", name, ed)
		}
	}
	wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/admin/webhooks", `{"channel_id":"`+f.ops.ID+`","name":"x","token":"mine"}`), http.StatusBadRequest, CodeBadRequest)

	// An archived channel takes no webhook, and a failed create leaves nothing behind.
	arch := f.admin.createChannel(t, "old", "public")
	wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/channels/"+arch.ID+"/archive", nil), http.StatusOK, "")
	apps := e.count(`SELECT COUNT(*) FROM apps`)
	wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/admin/webhooks", protocol.CreateWebhookRequest{ChannelID: arch.ID, Name: "x"}), http.StatusBadRequest, CodeBadRequest)
	if e.count(`SELECT COUNT(*) FROM apps`) != apps || e.count(`SELECT COUNT(*) FROM webhooks`) != 1 {
		t.Error("a rejected webhook left an app behind")
	}

	// Two webhooks with the same name get distinct bot usernames; a private
	// channel the admin created works too.
	second, _ := e.newWebhook(f.admin, f.ops.ID, "Deploy Hook")
	a1, _ := e.apps.GetApp(e.ctx, f.wh.AppID)
	a2, _ := e.apps.GetApp(e.ctx, second.AppID)
	if a1.Username == a2.Username || a2.Username != "deploy-hook-2" {
		t.Errorf("usernames = %q, %q", a1.Username, a2.Username)
	}
	e.newWebhook(f.admin, f.admin.createChannel(t, "secret", "private").ID, "Priv Hook")
	// A private channel the admin cannot see is no channel to them.
	hidden := f.alice.createChannel(t, "hidden", "private")
	ed := wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/admin/webhooks", protocol.CreateWebhookRequest{ChannelID: hidden.ID, Name: "x"}), http.StatusBadRequest, CodeBadRequest)
	if ed.FieldErrors()["channel_id"] == "" {
		t.Errorf("no reason for channel_id: %+v", ed)
	}
}

func TestWebhookPostsSlackPayloadAsPseudoApp(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()

	rec := e.hookJSON(f.token, `{"text":"Deploy *finished* <https://ci.home.arpa/1|build 1>","username":"  CI \u202eBot\u0007 ","icon_emoji":":rocket:","icon_url":"https://evil.example/a.png"}`)
	wantHookOK(t, rec)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}

	m := f.lastMessage(t)
	if m.Kind != "text" || m.Body != ":rocket: Deploy **finished** [build 1](<https://ci.home.arpa/1>)" {
		t.Errorf("message = %q (%s)", m.Body, m.Kind)
	}
	if m.OverrideName == nil || *m.OverrideName != "CI Bot" {
		t.Errorf("override_name = %v", m.OverrideName)
	}
	if m.Author.ID != f.botUserID || m.Author.Kind != "app" || m.Author.DisplayName != "Deploy Hook" || m.ChannelID != f.ops.ID {
		t.Errorf("author = %+v", m.Author)
	}
	if len(m.Mentions.UserIDs) != 0 || m.Mentions.Channel || m.Mentions.Here {
		t.Errorf("mentions = %+v", m.Mentions)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'message.created' AND channel_id = ?`, f.ops.ID); n < 1 {
		t.Error("no message.created event")
	}

	// A long username is cut; one that is only control characters is dropped.
	wantHookOK(t, e.hookJSON(f.token, `{"text":"a","username":"`+strings.Repeat("n", 100)+`"}`))
	if m = f.lastMessage(t); m.OverrideName == nil || len(*m.OverrideName) != 64 {
		t.Errorf("long override_name = %v", m.OverrideName)
	}
	wantHookOK(t, e.hookJSON(f.token, `{"text":"b","username":"\u0007\u202e"}`))
	if m = f.lastMessage(t); m.OverrideName != nil {
		t.Errorf("override_name = %q, want none", *m.OverrideName)
	}
}

// TestWebhookMentionsAreInert: no text a sender writes can notify anyone or
// pass for a real user.
func TestWebhookMentionsAreInert(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()

	// Control: a person's @mention does create mention rows.
	human := f.alice.post(t, f.ops.ID, "hello @admin and @channel")
	if c, _, rows := e.mentionState(human.ID); c != 1 || rows != 1 {
		t.Fatalf("control message mentions: channel %d rows %d", c, rows)
	}

	for name, payload := range map[string]string{
		"slack tokens":  `{"text":"<!channel> <!here> <!everyone> <@` + f.alice.id + `> <#C1|general> <!subteam^S1|@oncall> hi","username":"alice"}`,
		"plain text":    `{"text":"@channel @here @everyone @alice @admin hi","username":"admin"}`,
		"entity forms":  `{"text":"&lt;!channel&gt; &#64;channel @alice\n@here"}`,
		"in attachment": `{"attachments":[{"title":"@channel","text":"@alice and @admin","fields":[{"title":"@here","value":"@channel"}],"pretext":"@here @channel"}]}`,
		"in blocks":     `{"blocks":[{"type":"header","text":{"type":"plain_text","text":"@channel"}},{"type":"section","text":{"type":"mrkdwn","text":"@alice @admin @here"}}]}`,
		"in code":       "{\"text\":\"`@channel` @here ```\\n@alice\\n```\"}",
		"markdown":      `{"text":"**@channel** [@alice](https://x.example) @admin"}`,
	} {
		wantHookOK(t, e.hookJSON(f.token, payload))
		m := f.lastMessage(t)
		if c, h, rows := e.mentionState(m.ID); c != 0 || h != 0 || rows != 0 {
			t.Errorf("%s: mention_channel %d mention_here %d rows %d in %q", name, c, h, rows, m.Body)
		}
		if m.Mentions.Channel || m.Mentions.Here || len(m.Mentions.UserIDs) != 0 {
			t.Errorf("%s: mentions = %+v", name, m.Mentions)
		}
		if m.Author.ID != f.botUserID {
			t.Errorf("%s: author = %s, want the pseudo-app", name, m.Author.ID)
		}
	}

	// A username of a real user is a label on the bot's message, nothing more.
	wantHookOK(t, e.hookJSON(f.token, `{"text":"hi","username":"alice"}`))
	m := f.lastMessage(t)
	if m.OverrideName == nil || *m.OverrideName != "alice" || m.Author.ID == f.alice.id || m.Author.Username == "alice" || m.Author.AvatarURL != nil {
		t.Errorf("message = %+v", m)
	}
}

func TestWebhookDangerousLinksAreNotLinked(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	wantHookOK(t, e.hookJSON(f.token, `{"text":"<javascript:alert(1)|click me> <JaVaScRiPt:alert(2)> <data:text/html,x|d> <vbscript:x|v> <file:///etc/passwd|f> [md](javascript:alert(3)) &lt;script&gt;alert(4)&lt;/script&gt; <img src=x onerror=alert(5)>",`+
		`"attachments":[{"title":"t","title_link":"javascript:alert(6)","text":"x"}],`+
		`"blocks":[{"type":"image","image_url":"javascript:alert(7)","alt_text":"i"},{"type":"actions","elements":[{"type":"button","text":{"type":"plain_text","text":"b"},"url":"javascript:alert(8)"}]}]}`))
	body := f.lastMessage(t).Body
	// Every link is a real one to http, https or mailto, and no < is raw.
	for i := 0; i < len(body); i++ {
		switch {
		case strings.HasPrefix(body[i:], "](") && !isEscaped(body, i):
			if rest := strings.ToLower(body[i+2:]); !strings.HasPrefix(rest, "<http") && !strings.HasPrefix(rest, "<mailto") {
				t.Errorf("link to %.30q in:\n%s", body[i:], body)
			}
		case body[i] == '<' && !isEscaped(body, i) && !strings.HasSuffix(body[:i], "]("):
			t.Errorf("raw < at %d in:\n%s", i, body)
		}
	}
	for _, bad := range []string{"[click", "[t]", "[md]", "[i]", "[b]"} {
		if strings.Contains(body, bad) {
			t.Errorf("body contains %q:\n%s", bad, body)
		}
	}
	for _, want := range []string{"click me", "JaVaScRiPt:alert(2)", `\<script\>`, `\<img src=x onerror=alert(5)\>`, "**t**"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
}

func isEscaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

func TestWebhookRevokedAndUnknownTokensAre404(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	b := e.newBot(f.admin, "bot", protocol.AppScopes, nil, f.ops.ID)

	for name, token := range map[string]string{
		"unknown":          "bwh_" + strings.Repeat("a", 52),
		"wrong prefix":     "bub_" + strings.Repeat("a", 52),
		"app token":        b.token,
		"truncated secret": f.token[:len(f.token)-1],
		"extended secret":  f.token + "a",
		"upper case":       strings.ToUpper(f.token),
		"too long":         "bwh_" + strings.Repeat("a", 500),
		"no prefix":        f.token[4:],
	} {
		ed := wantStatus(t, e.hookJSON(token, `{"text":"x"}`), http.StatusNotFound, CodeNotFound)
		if strings.Contains(ed.Message, token) {
			t.Errorf("%s: the answer repeats the token", name)
		}
	}
	// Unknown tokens are 404 before the body is read, whatever it says.
	wantStatus(t, e.hook("bwh_"+strings.Repeat("b", 52), "text/plain", "garbage"), http.StatusNotFound, CodeNotFound)

	wantHookOK(t, e.hookJSON(f.token, `{"text":"live"}`))
	wantStatus(t, f.admin.do(http.MethodDelete, "/api/v1/admin/webhooks/"+f.wh.ID, nil), http.StatusNoContent, "")
	wantStatus(t, e.hookJSON(f.token, `{"text":"revoked"}`), http.StatusNotFound, CodeNotFound)
	wantStatus(t, e.hookJSON(f.token, `{"alert":{"severity":"info","title":"t","dedupe_key":"k"}}`), http.StatusNotFound, CodeNotFound)
	if e.count(`SELECT COUNT(*) FROM messages WHERE body = 'revoked'`) != 0 || e.count(`SELECT COUNT(*) FROM alerts`) != 0 {
		t.Error("a revoked webhook posted")
	}
}

func TestWebhookStopsWhenItsPseudoAppIsDisabledOrLosesTheChannel(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	wantHookOK(t, e.hookJSON(f.token, `{"text":"1"}`))

	yes, no := true, false
	wantStatus(t, f.admin.do(http.MethodPatch, "/api/v1/admin/apps/"+f.wh.AppID, protocol.UpdateAppRequest{Disabled: &yes}), http.StatusOK, "")
	wantStatus(t, e.hookJSON(f.token, `{"text":"2"}`), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.admin.do(http.MethodPatch, "/api/v1/admin/apps/"+f.wh.AppID, protocol.UpdateAppRequest{Disabled: &no}), http.StatusOK, "")
	wantHookOK(t, e.hookJSON(f.token, `{"text":"3"}`))

	wantStatus(t, f.admin.do(http.MethodDelete, "/api/v1/channels/"+f.ops.ID+"/members/"+f.botUserID, nil), http.StatusNoContent, "")
	wantStatus(t, e.hookJSON(f.token, `{"text":"4"}`), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.admin.do(http.MethodPut, "/api/v1/channels/"+f.ops.ID+"/members/"+f.botUserID, nil), http.StatusOK, "")
	wantHookOK(t, e.hookJSON(f.token, `{"text":"5"}`))

	// An archived channel is read-only.
	wantStatus(t, f.admin.do(http.MethodPost, "/api/v1/channels/"+f.ops.ID+"/archive", nil), http.StatusOK, "")
	wantStatus(t, e.hookJSON(f.token, `{"text":"6"}`), http.StatusConflict, CodeConflict)
	wantStatus(t, e.hookJSON(f.token, `{"alert":{"severity":"info","title":"t","dedupe_key":"k"}}`), http.StatusConflict, CodeConflict)
}

func TestWebhookRateLimitIs30PerMinuteBurst10(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	_, other := e.newWebhook(f.admin, f.ops.ID, "Other")

	for i := 0; i < 10; i++ {
		wantHookOK(t, e.hookJSON(f.token, `{"text":"m`+strconv.Itoa(i)+`"}`))
	}
	rec := e.hookJSON(f.token, `{"text":"over"}`)
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 2 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if e.count(`SELECT COUNT(*) FROM messages WHERE body = 'over'`) != 0 {
		t.Error("a limited post was stored")
	}

	// The limit is per webhook, and refills at 30 per minute.
	wantHookOK(t, e.hookJSON(other, `{"text":"other"}`))
	e.clock.Advance(2 * time.Second)
	wantHookOK(t, e.hookJSON(f.token, `{"text":"again"}`))
	wantStatus(t, e.hookJSON(f.token, `{"text":"over again"}`), http.StatusTooManyRequests, CodeRateLimited)
	e.clock.Advance(time.Minute)
	for i := 0; i < 10; i++ {
		wantHookOK(t, e.hookJSON(f.token, `{"text":"refilled"}`))
	}
	wantStatus(t, e.hookJSON(f.token, `{"text":"over"}`), http.StatusTooManyRequests, CodeRateLimited)
}

func TestWebhookAlertPassthroughCreatesAndDedupes(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	doc := func(sev, text string) string {
		return `{"text":"top-level","username":"ignored","alert":{"severity":"` + sev + `","title":"Disk almost full","text":"` + text +
			`","fields":[{"name":"Host","value":"nas-1","short":true}],"link":"https://nas.home.arpa/disk","dedupe_key":"disk-nas-1","renotify_after_s":600}}`
	}

	wantHookOK(t, e.hookJSON(f.token, doc("warning", "92% used <!channel> @channel")))
	rec := f.alice.do(http.MethodGet, "/api/v1/alerts?state=open", nil)
	wantStatus(t, rec, http.StatusOK, "")
	page := decodeBody[protocol.AlertPage](t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("open alerts = %d", len(page.Items))
	}
	a := page.Items[0]
	if a.Kind != "alert" || a.Alert == nil || a.Alert.Severity != "warning" || a.Alert.Occurrences != 1 || a.Alert.DedupeKey != "disk-nas-1" || a.Alert.Title != "Disk almost full" ||
		a.Author.ID != f.botUserID || a.ChannelID != f.ops.ID || a.OverrideName != nil || a.Alert.Link == nil || len(a.Alert.Fields) != 1 {
		t.Fatalf("alert = %+v / %+v", a, a.Alert)
	}
	if c, h, rows := e.mentionState(a.ID); c != 0 || h != 0 || rows != 0 {
		t.Errorf("new alert mentions: channel %d here %d rows %d", c, h, rows)
	}

	// The same key updates the one message, raises the severity and stays quiet.
	wantHookOK(t, e.hookJSON(f.token, doc("critical", "95% used @channel @here @alice")))
	rec = f.alice.do(http.MethodGet, "/api/v1/alerts?state=open", nil)
	wantStatus(t, rec, http.StatusOK, "")
	page = decodeBody[protocol.AlertPage](t, rec)
	if len(page.Items) != 1 || page.Items[0].ID != a.ID || page.Items[0].Alert.Occurrences != 2 || page.Items[0].Alert.Severity != "critical" ||
		!strings.Contains(page.Items[0].Body, "95% used") {
		t.Fatalf("after repeat: %+v", page.Items)
	}
	if c, h, rows := e.mentionState(a.ID); c != 0 || h != 0 || rows != 0 {
		t.Errorf("updated alert mentions: channel %d here %d rows %d", c, h, rows)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'alert'`); n != 1 {
		t.Errorf("alert messages = %d", n)
	}

	// A different key is a different alert; the top-level text fills a missing alert text.
	wantHookOK(t, e.hookJSON(f.token, `{"text":"see dashboard","alert":{"severity":"info","title":"Other","dedupe_key":"other"}}`))
	if n := e.count(`SELECT COUNT(*) FROM alerts WHERE state = 'open'`); n != 2 {
		t.Errorf("open alerts = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'alert' AND body LIKE '%see dashboard%'`); n != 1 {
		t.Error("the top-level text did not become the alert text")
	}

	// Alert problems are reported against the alert fields.
	for payload, field := range map[string]string{
		`{"alert":{"severity":"info","title":"t"}}`:                                               "alert.dedupe_key",
		`{"alert":{"severity":"loud","title":"t","dedupe_key":"k"}}`:                              "alert.severity",
		`{"alert":{"severity":"info","dedupe_key":"k"}}`:                                          "alert.title",
		`{"alert":{"severity":"info","title":"t","dedupe_key":"k","link":"javascript:alert(1)"}}`: "alert.link",
	} {
		ed := wantStatus(t, e.hookJSON(f.token, payload), http.StatusBadRequest, CodeBadRequest)
		if ed.FieldErrors()[field] == "" {
			t.Errorf("%s: fields = %v", payload, ed.FieldErrors())
		}
	}
	wantStatus(t, e.hookJSON(f.token, `{"alert":"boom"}`), http.StatusBadRequest, CodeBadRequest)
}

func TestWebhookFormEncodedPayload(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	const form = "application/x-www-form-urlencoded"

	fixture, err := os.ReadFile(filepath.Join("..", "hooks", "testdata", "form-payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantHookOK(t, e.hook(f.token, form, string(fixture)))
	m := f.lastMessage(t)
	if !strings.HasPrefix(m.Body, ":floppy_disk: Nightly backup **finished**") || !strings.Contains(m.Body, "[run 812](<https://backup.home.arpa/runs/812>)") ||
		!strings.Contains(m.Body, "Backup OK") || m.OverrideName == nil || *m.OverrideName != "Backup" {
		t.Errorf("message = %q, override %v", m.Body, m.OverrideName)
	}
	if c, h, rows := e.mentionState(m.ID); c != 0 || h != 0 || rows != 0 {
		t.Errorf("mentions: %d %d %d", c, h, rows)
	}
	// A form with other fields and a charset parameter works too.
	wantHookOK(t, e.hook(f.token, form+"; charset=UTF-8", "token=ignored&payload=%7B%22text%22%3A%22plus+and+%25%22%7D"))
	if m = f.lastMessage(t); m.Body != "plus and %" {
		t.Errorf("body = %q", m.Body)
	}
	wantStatus(t, e.hook(f.token, form, "text=hello"), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.hook(f.token, form, "payload="), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.hook(f.token, form, "payload=not+json"), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.hook(f.token, form, ""), http.StatusBadRequest, CodeBadRequest)
}

func TestWebhookBodyHandling(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()

	for name, tc := range map[string]struct {
		contentType, body string
		status            int
		code              string
	}{
		"no content type":  {"", `{"text":"x"}`, http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"text/plain":       {"text/plain", `{"text":"x"}`, http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"xml":              {"application/xml", `<a/>`, http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"multipart":        {"multipart/form-data; boundary=x", "--x--", http.StatusUnsupportedMediaType, CodeUnsupportedMedia},
		"empty body":       {"application/json", ``, http.StatusBadRequest, CodeBadRequest},
		"not json":         {"application/json", `hello`, http.StatusBadRequest, CodeBadRequest},
		"truncated json":   {"application/json", `{"text":`, http.StatusBadRequest, CodeBadRequest},
		"array":            {"application/json", `["x"]`, http.StatusBadRequest, CodeBadRequest},
		"string":           {"application/json", `"x"`, http.StatusBadRequest, CodeBadRequest},
		"empty object":     {"application/json", `{}`, http.StatusBadRequest, CodeBadRequest},
		"blank text":       {"application/json", `{"text":"   "}`, http.StatusBadRequest, CodeBadRequest},
		"only mentions":    {"application/json", `{"text":"<!channel>"}`, http.StatusBadRequest, CodeBadRequest},
		"two documents":    {"application/json", `{"text":"a"}{"text":"b"}`, http.StatusBadRequest, CodeBadRequest},
		"too large":        {"application/json", `{"text":"` + strings.Repeat("a", 256<<10) + `"}`, http.StatusRequestEntityTooLarge, CodeTooLarge},
		"charset ok":       {"application/json; charset=utf-8", `{"text":"fine"}`, http.StatusOK, ""},
		"unknown keys ok":  {"application/json", `{"text":"fine","channel":"#x","link_names":1,"unfurl_links":true,"thread_ts":"1.2"}`, http.StatusOK, ""},
		"numbers as text":  {"application/json", `{"attachments":[{"fields":[{"title":3,"value":4.5}]}]}`, http.StatusOK, ""},
		"hostile nesting":  {"application/json", `{"text":"x","blocks":[` + strings.Repeat(`{"type":"context","elements":[`, 50) + strings.Repeat(`]}`, 50) + `]}`, http.StatusOK, ""},
		"unknown block":    {"application/json", `{"blocks":[{"type":"mystery"}]}`, http.StatusOK, ""},
		"bom is not json":  {"application/json", "\ufeff" + `{"text":"x"}`, http.StatusBadRequest, CodeBadRequest},
		"binary":           {"application/json", "\x00\x01\x02", http.StatusBadRequest, CodeBadRequest},
		"null":             {"application/json", `null`, http.StatusBadRequest, CodeBadRequest},
		"number":           {"application/json", `12`, http.StatusBadRequest, CodeBadRequest},
		"alert is a list":  {"application/json", `{"alert":[]}`, http.StatusBadRequest, CodeBadRequest},
		"whitespace only":  {"application/json", " \n\t ", http.StatusBadRequest, CodeBadRequest},
		"deep object text": {"application/json", `{"text":{"a":1}}`, http.StatusBadRequest, CodeBadRequest},
	} {
		e.clock.Advance(2 * time.Second) // one more post of the webhook's limit
		rec := e.hook(f.token, tc.contentType, tc.body)
		if tc.status == http.StatusOK {
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.status, rec.Body.String())
			continue
		}
		if ed := decodeError(t, rec); ed.Code != tc.code {
			t.Errorf("%s: code %q, want %q", name, ed.Code, tc.code)
		}
	}
	// Bodies are never echoed back.
	rec := e.hookJSON(f.token, `{"text":"SECRET-BODY","alert":"x"}`)
	if strings.Contains(rec.Body.String(), "SECRET-BODY") {
		t.Error("the answer echoes the body")
	}

	// A long text is cut to the message limit, not refused.
	e.clock.Advance(2 * time.Second)
	wantHookOK(t, e.hookJSON(f.token, `{"text":"`+strings.Repeat("word ", 4000)+`"}`))
	last := f.lastMessage(t)
	if n := len([]rune(last.Body)); n > 8000 || !strings.HasSuffix(last.Body, "(truncated)") {
		t.Errorf("long message: %d characters, ends %q", n, last.Body[max(0, len(last.Body)-20):])
	}
}

func TestWebhookIsOpenToSendersNotBrowsersOrOutsiders(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()

	// No cookie, no Origin: a sender. A foreign Origin does not matter either,
	// and a session cookie is not needed or consulted.
	wantHookOK(t, e.hookJSON(f.token, `{"text":"plain sender"}`))
	wantHookOK(t, e.hook(f.token, "application/json", `{"text":"with origin"}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }))
	wantHookOK(t, e.hook(f.token, "application/json", `{"text":"with bearer"}`, func(r *http.Request) { r.Header.Set("Authorization", "Bearer bub_"+strings.Repeat("a", 52)) }))

	// The network gate applies like everywhere: a public address is refused
	// before the token is looked at.
	rec := e.client("8.8.8.8").do(http.MethodPost, "/hooks/"+f.token, `{"text":"outsider"}`)
	wantStatus(t, rec, http.StatusForbidden, CodeForbidden)
	rec = e.client("8.8.8.8").do(http.MethodPost, "/hooks/bwh_"+strings.Repeat("c", 52), `{"text":"outsider"}`)
	wantStatus(t, rec, http.StatusForbidden, CodeForbidden)
	if e.count(`SELECT COUNT(*) FROM messages WHERE body = 'outsider'`) != 0 {
		t.Error("an outsider posted")
	}
}

func TestWebhookDoesNotLogOrAuditBodies(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	wantHookOK(t, e.hookJSON(f.token, `{"text":"CONFIDENTIAL-TEXT","alert":null}`))
	wantHookOK(t, e.hookJSON(f.token, `{"alert":{"severity":"info","title":"CONFIDENTIAL-ALERT","dedupe_key":"k"}}`))
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE detail LIKE '%CONFIDENTIAL%'`); n != 0 {
		t.Errorf("%d audit entries carry message text", n)
	}
}

func TestContractWebhookRoutesAreDocumented(t *testing.T) {
	doc, _ := loadSpec(t)
	for _, want := range []struct {
		method, path string
		cookie       bool
		anonymous    bool
	}{
		{"GET", "/admin/webhooks", true, false},
		{"POST", "/admin/webhooks", true, false},
		{"DELETE", "/admin/webhooks/{id}", true, false},
		{"POST", "/hooks/{token}", false, true},
	} {
		item := doc.Paths.Find(want.path)
		if item == nil || item.GetOperation(want.method) == nil {
			t.Errorf("%s %s is not in docs/openapi.yaml", want.method, want.path)
			continue
		}
		op := item.GetOperation(want.method)
		var cookie, bearer bool
		if op.Security != nil {
			for _, req := range *op.Security {
				_, c := req["cookieAuth"]
				_, b := req["bearerAuth"]
				cookie, bearer = cookie || c, bearer || b
			}
		}
		if cookie != want.cookie || bearer {
			t.Errorf("%s %s: security cookie=%v bearer=%v, want cookie=%v and no bearer", want.method, want.path, cookie, bearer, want.cookie)
		}
		if want.anonymous && (op.Security == nil || len(*op.Security) != 0) {
			t.Errorf("%s %s should declare security: []", want.method, want.path)
		}
		if want.anonymous && (op.Servers == nil || len(*op.Servers) != 1 || (*op.Servers)[0].URL != "/") {
			t.Errorf("%s %s should be served at the root", want.method, want.path)
		}
	}
	hook := doc.Paths.Find("/hooks/{token}").GetOperation("POST")
	for _, ct := range []string{"application/json", "application/x-www-form-urlencoded"} {
		if hook.RequestBody == nil || hook.RequestBody.Value.Content.Get(ct) == nil {
			t.Errorf("POST /hooks/{token} does not document %s", ct)
		}
	}
	for _, status := range []string{"200", "400", "404", "413", "415", "429"} {
		if hook.Responses.Status(atoi(status)) == nil {
			t.Errorf("POST /hooks/{token} does not document %s", status)
		}
	}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// lockedBuf is a goroutine-safe log sink.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func capturingLogger() (*slog.Logger, *lockedBuf) {
	buf := &lockedBuf{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// A 500 and a panic on the hook path must not put the secret URL token or the
// payload into the server log.
func TestWebhookTokenAndBodyNeverReachTheLog(t *testing.T) {
	const secretBody = "TOP-SECRET-PAYLOAD-7731"
	logger, logs := capturingLogger()
	e := newEnvLogged(t, logger)
	f := e.hookFixture()
	body := `{"text":"` + secretBody + `"}`
	wantHookOK(t, e.hookJSON(f.token, body))

	// 500: the database is gone, so the token lookup fails with an internal error.
	e.db.Close()
	rec := e.hookJSON(f.token, body)
	wantStatus(t, rec, http.StatusInternalServerError, CodeInternal)

	// Panic: a handler that panics on the same kind of request, behind the
	// real recoverer.
	h := recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom while handling a hook")
	}))
	req := httptest.NewRequest(http.MethodPost, "/hooks/"+f.token, strings.NewReader(body))
	prec := httptest.NewRecorder()
	h.ServeHTTP(prec, req)
	wantStatus(t, prec, http.StatusInternalServerError, CodeInternal)

	out := logs.String()
	if out == "" {
		t.Fatal("nothing was logged for the 500 and the panic")
	}
	for _, secret := range []string{f.token, strings.TrimPrefix(f.token, protocol.WebhookTokenPrefix), secretBody} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains %q", secret)
		}
	}
}

// Dedupe keys are scoped to the posting webhook (pseudo-app): the same key
// from two webhooks is two alerts, not one.
func TestWebhookSameDedupeKeyFromTwoWebhooksMakesTwoAlerts(t *testing.T) {
	e := newEnv(t)
	f := e.hookFixture()
	_, token2 := e.newWebhook(f.admin, f.ops.ID, "Second Hook")
	const doc = `{"alert":{"severity":"warning","title":"Disk","dedupe_key":"shared-key"}}`
	wantHookOK(t, e.hookJSON(f.token, doc))
	wantHookOK(t, e.hookJSON(token2, doc))
	if n := e.count(`SELECT COUNT(*) FROM alerts WHERE state = 'open'`); n != 2 {
		t.Errorf("open alerts = %d, want 2", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM messages WHERE kind = 'alert'`); n != 2 {
		t.Errorf("alert messages = %d, want 2", n)
	}
	// Each webhook still dedupes against itself.
	wantHookOK(t, e.hookJSON(f.token, doc))
	if n := e.count(`SELECT COUNT(*) FROM alerts WHERE state = 'open'`); n != 2 {
		t.Errorf("open alerts after a repeat = %d, want 2", n)
	}
}
