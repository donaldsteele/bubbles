package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// alertFixture is an admin, a member, a channel #ops with both and an
// alerting app that is a member of #ops.
type alertFixture struct {
	admin, alice member
	ops          protocol.Channel
	bot          bot
}

func (e *env) alertFixture() alertFixture {
	e.t.Helper()
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	ops := admin.createChannel(e.t, "ops", "public", alice)
	return alertFixture{admin: admin, alice: alice, ops: ops, bot: e.newBot(admin, "netdiag", []string{"alerts:write"}, nil, ops.ID)}
}

func alertIn(mods ...func(*protocol.AlertInput)) protocol.AlertInput {
	in := protocol.AlertInput{Channel: "ops", Severity: "warning", Title: "Disk almost full", Text: "92% used", DedupeKey: "disk"}
	for _, m := range mods {
		m(&in)
	}
	return in
}

func (f alertFixture) post(e *env, in protocol.AlertInput) *httptest.ResponseRecorder {
	e.t.Helper()
	return f.bot.do(e, http.MethodPost, "/api/v1/alerts", in)
}

func TestAlertsHTTPLifecycle(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()

	rec := f.post(e, alertIn())
	wantStatus(t, rec, http.StatusCreated, "")
	created := decodeBody[protocol.PostedAlert](t, rec)
	msg := created.Message
	if created.Transition != "created" || msg.Kind != "alert" || msg.Alert == nil || msg.Alert.State != "open" || msg.Alert.Occurrences != 1 ||
		msg.Author.ID != f.bot.app.UserID || msg.ChannelID != f.ops.ID || !strings.Contains(msg.Body, "Disk almost full") {
		t.Fatalf("created = %+v", created)
	}

	// A repeat updates the same message: 200, occurrences up.
	rec = f.post(e, alertIn())
	wantStatus(t, rec, http.StatusOK, "")
	again := decodeBody[protocol.PostedAlert](t, rec)
	if again.Transition != "updated_other" || again.Message.ID != msg.ID || again.Message.Alert.Occurrences != 2 {
		t.Errorf("repeat = %+v", again)
	}
	// An escalation is announced as such.
	rec = f.post(e, alertIn(func(in *protocol.AlertInput) { in.Severity = "critical" }))
	wantStatus(t, rec, http.StatusOK, "")
	if up := decodeBody[protocol.PostedAlert](t, rec); up.Transition != "escalated" || up.Message.Alert.Severity != "critical" {
		t.Errorf("escalation = %+v", up)
	}

	// The member sees the alert in the list and in the channel, and acknowledges it.
	rec = f.alice.do(http.MethodGet, "/api/v1/alerts?state=open", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if page := decodeBody[protocol.AlertPage](t, rec); len(page.Items) != 1 || page.Items[0].ID != msg.ID || page.NextCursor != nil {
		t.Errorf("open alerts = %+v", page)
	}
	rec = f.alice.do(http.MethodPost, "/api/v1/alerts/"+msg.ID+"/ack", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if acked := decodeBody[protocol.Message](t, rec); acked.Alert.State != "acked" || acked.Alert.AckedBy == nil || *acked.Alert.AckedBy != f.alice.id {
		t.Errorf("acked = %+v", acked.Alert)
	}
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/"+msg.ID+"/ack", nil), http.StatusOK, "")
	// Listing returns message bodies, so the app needs messages:read as well.
	reader := e.newBot(f.admin, "reader", []string{"alerts:write", "messages:read"}, nil, f.ops.ID)
	rec = reader.do(e, http.MethodGet, "/api/v1/alerts?state=acked", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if page := decodeBody[protocol.AlertPage](t, rec); len(page.Items) != 1 {
		t.Errorf("bot's acked alerts = %+v", page)
	}

	// The app resolves by key; a thread reply says so.
	rec = f.bot.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk", Text: "back to 60%"})
	wantStatus(t, rec, http.StatusOK, "")
	if res := decodeBody[protocol.Message](t, rec); res.Alert.State != "resolved" || res.ReplyCount != 1 {
		t.Errorf("resolved = %+v reply_count %d", res.Alert, res.ReplyCount)
	}
	rec = f.alice.do(http.MethodGet, "/api/v1/messages/"+msg.ID+"/thread", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if th := decodeBody[protocol.Thread](t, rec); len(th.Replies) != 1 || th.Replies[0].Body != "Resolved by netdiag: back to 60%" || th.Replies[0].Kind != "system" {
		t.Errorf("thread = %+v", th.Replies)
	}
	wantStatus(t, f.bot.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk"}), http.StatusOK, "")
	wantStatus(t, f.bot.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "never"}), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/"+msg.ID+"/ack", nil), http.StatusConflict, CodeConflict)

	// The same key now opens a new alert.
	rec = f.post(e, alertIn())
	wantStatus(t, rec, http.StatusCreated, "")
	if next := decodeBody[protocol.PostedAlert](t, rec); next.Message.ID == msg.ID || next.Message.Alert.Occurrences != 1 {
		t.Errorf("alert after resolve = %+v", next)
	}

	// A member resolves by id, with or without a body.
	second := decodeBody[protocol.PostedAlert](t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.DedupeKey = "cpu" }))).Message
	rec = f.alice.do(http.MethodPost, "/api/v1/alerts/"+second.ID+"/resolve", nil)
	wantStatus(t, rec, http.StatusOK, "")
	third := decodeBody[protocol.PostedAlert](t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.DedupeKey = "mem" }))).Message
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/"+third.ID+"/resolve", protocol.ResolveAlertRequest{Text: "restarted"}), http.StatusOK, "")

	for _, action := range []string{"alert.ack", "alert.resolve"} {
		if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action); n < 1 {
			t.Errorf("no %s audit entry", action)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'alert.resolve' AND actor_kind = 'app' AND actor_id = ?`, f.bot.app.UserID); n != 1 {
		t.Errorf("app resolve audit entries = %d", n)
	}
}

func TestAlertsHTTPPermissions(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()
	bob := e.newMember("bob", "192.168.1.21")
	other := f.admin.createChannel(t, "other", "public")
	anon := e.client("")

	// Anonymous callers are not signed in.
	wantStatus(t, anon.do(http.MethodPost, "/api/v1/alerts", alertIn()), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, anon.do(http.MethodGet, "/api/v1/alerts", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, anon.do(http.MethodPost, "/api/v1/alerts/x/ack", nil), http.StatusUnauthorized, CodeUnauthorized)

	// Only apps post alerts; a person with a session cannot.
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts", alertIn()), http.StatusForbidden, CodeForbidden)
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk"}), http.StatusForbidden, CodeForbidden)

	// A token without alerts:write is refused, however else it is scoped.
	noScope := e.newBot(f.admin, "noscope", []string{"messages:write", "messages:read", "channels:read"}, nil, f.ops.ID)
	wantStatus(t, noScope.do(e, http.MethodPost, "/api/v1/alerts", alertIn()), http.StatusForbidden, CodeForbidden)
	wantStatus(t, noScope.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk"}), http.StatusForbidden, CodeForbidden)
	if n := e.count(`SELECT COUNT(*) FROM alerts`); n != 0 {
		t.Fatalf("refused posts created %d alerts", n)
	}

	// The app must be a member of the channel, and the token may further restrict it.
	wantStatus(t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.Channel = "other" })), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.Channel = other.ID })), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.Channel = "nowhere" })), http.StatusNotFound, CodeNotFound)
	restricted := e.newBot(f.admin, "restricted", []string{"alerts:write"}, []string{other.ID}, f.ops.ID, other.ID)
	wantStatus(t, restricted.do(e, http.MethodPost, "/api/v1/alerts", alertIn()), http.StatusNotFound, CodeNotFound)
	wantStatus(t, restricted.do(e, http.MethodPost, "/api/v1/alerts", alertIn(func(in *protocol.AlertInput) { in.Channel = "other"; in.DedupeKey = "rk" })), http.StatusCreated, "")

	// People outside the channel can neither see nor touch its alerts.
	msg := decodeBody[protocol.PostedAlert](t, f.post(e, alertIn())).Message
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/alerts/"+msg.ID+"/ack", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/alerts/"+msg.ID+"/resolve", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, bob.do(http.MethodPost, "/api/v1/alerts/nope/ack", nil), http.StatusNotFound, CodeNotFound)
	rec := bob.do(http.MethodGet, "/api/v1/alerts", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if page := decodeBody[protocol.AlertPage](t, rec); len(page.Items) != 0 || page.Items == nil {
		t.Errorf("outsider's alert list = %+v", page)
	}
	// Another app cannot resolve this app's alert by key (keys are per app).
	rest := restricted.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk"})
	wantStatus(t, rest, http.StatusNotFound, CodeNotFound)
	// A text message is no alert.
	text := f.alice.post(t, f.ops.ID, "plain")
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/"+text.ID+"/ack", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, f.alice.do(http.MethodPost, "/api/v1/alerts/"+text.ID+"/resolve", nil), http.StatusNotFound, CodeNotFound)
}

func TestAlertsHTTPValidation(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()
	tests := []struct {
		name  string
		mod   func(*protocol.AlertInput)
		field string
	}{
		{"no title", func(in *protocol.AlertInput) { in.Title = "" }, "title"},
		{"long title", func(in *protocol.AlertInput) { in.Title = strings.Repeat("t", 201) }, "title"},
		{"long text", func(in *protocol.AlertInput) { in.Text = strings.Repeat("t", 4001) }, "text"},
		{"bad severity", func(in *protocol.AlertInput) { in.Severity = "urgent" }, "severity"},
		{"no key", func(in *protocol.AlertInput) { in.DedupeKey = "" }, "dedupe_key"},
		{"long key", func(in *protocol.AlertInput) { in.DedupeKey = strings.Repeat("k", 201) }, "dedupe_key"},
		{"javascript link", func(in *protocol.AlertInput) { l := "javascript:alert(1)"; in.Link = &l }, "link"},
		{"ftp link", func(in *protocol.AlertInput) { l := "ftp://nas/x"; in.Link = &l }, "link"},
		{"too many fields", func(in *protocol.AlertInput) { in.Fields = make([]protocol.AlertField, 21) }, "fields"},
		{"long field value", func(in *protocol.AlertInput) {
			in.Fields = []protocol.AlertField{{Name: "n", Value: strings.Repeat("v", 513)}}
		}, "fields[0]"},
		{"renotify range", func(in *protocol.AlertInput) { n := 3; in.RenotifyAfterS = &n }, "renotify_after_s"},
	}
	for _, tc := range tests {
		ed := wantStatus(t, f.post(e, alertIn(tc.mod)), http.StatusBadRequest, CodeBadRequest)
		if ed.FieldErrors()[tc.field] == "" {
			t.Errorf("%s: no reason for %s in %+v", tc.name, tc.field, ed)
		}
	}
	// Unknown fields and the wrong media type are refused like everywhere else.
	wantStatus(t, f.bot.do(e, http.MethodPost, "/api/v1/alerts", `{"channel":"ops","severity":"info","title":"t","dedupe_key":"k","bogus":1}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, e.client("192.168.1.60").do(http.MethodPost, "/api/v1/alerts", "x", asBearer(f.bot.token), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }), http.StatusUnsupportedMediaType, CodeUnsupportedMedia)
	// The limits themselves pass.
	wantStatus(t, f.post(e, alertIn(func(in *protocol.AlertInput) {
		in.Title = strings.Repeat("t", 200)
		in.Text = strings.Repeat("x", 4000)
		in.DedupeKey = strings.Repeat("k", 200)
		in.Fields = make([]protocol.AlertField, 20)
		for i := range in.Fields {
			in.Fields[i] = protocol.AlertField{Name: strings.Repeat("n", 64), Value: strings.Repeat("v", 512), Short: i%2 == 0}
		}
		l := "https://nas.local/disk"
		in.Link = &l
		five := 3600
		in.RenotifyAfterS = &five
		now := e.clock.Now().Format(time.RFC3339)
		in.OccurredAt = &now
	})), http.StatusCreated, "")
}

func TestAlertsHTTPListParameters(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()
	for i := range 3 {
		wantStatus(t, f.post(e, alertIn(func(in *protocol.AlertInput) { in.DedupeKey = "k" + strconv.Itoa(i) })), http.StatusCreated, "")
	}
	rec := f.alice.do(http.MethodGet, "/api/v1/alerts?limit=2&channel_id="+f.ops.ID, nil)
	wantStatus(t, rec, http.StatusOK, "")
	page := decodeBody[protocol.AlertPage](t, rec)
	if len(page.Items) != 2 || page.NextCursor == nil || page.Items[0].ID <= page.Items[1].ID {
		t.Fatalf("page 1 = %+v", page)
	}
	rec = f.alice.do(http.MethodGet, "/api/v1/alerts?limit=2&cursor="+*page.NextCursor, nil)
	wantStatus(t, rec, http.StatusOK, "")
	if next := decodeBody[protocol.AlertPage](t, rec); len(next.Items) != 1 || next.NextCursor != nil {
		t.Errorf("page 2 = %+v", next)
	}
	for _, q := range []string{"state=bogus", "state=open&state=acked", "limit=0", "limit=101", "limit=x", "cursor=" + strings.Repeat("c", 65), "channel_id=", "state="} {
		wantStatus(t, f.alice.do(http.MethodGet, "/api/v1/alerts?"+q, nil), http.StatusBadRequest, CodeBadRequest)
	}
	// An app without alerts:write or messages:read may not list.
	other := e.newBot(f.admin, "other", []string{"files:write"}, nil, f.ops.ID)
	wantStatus(t, other.do(e, http.MethodGet, "/api/v1/alerts", nil), http.StatusForbidden, CodeForbidden)
}

func TestAlertsHTTPRateLimitIs60PerMinuteBurst20(t *testing.T) {
	e := newEnv(t)
	f := e.alertFixture()
	other := e.newBot(f.admin, "other", []string{"alerts:write"}, nil, f.ops.ID)
	for i := range 20 {
		if rec := f.post(e, alertIn()); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("alert %d = %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := f.post(e, alertIn())
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// Resolve by key is ingest traffic too; the limit is per app, not shared.
	wantStatus(t, f.bot.do(e, http.MethodPost, "/api/v1/alerts/resolve", protocol.ResolveAlertByKeyRequest{DedupeKey: "disk"}), http.StatusTooManyRequests, CodeRateLimited)
	wantStatus(t, other.do(e, http.MethodPost, "/api/v1/alerts", alertIn()), http.StatusCreated, "")
	// 60 per minute is one per second.
	e.clock.Advance(time.Second)
	wantStatus(t, f.post(e, alertIn()), http.StatusOK, "")
	wantStatus(t, f.post(e, alertIn()), http.StatusTooManyRequests, CodeRateLimited)
}
