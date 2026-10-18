package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// pairOnce runs a whole pairing over HTTP: request (optionally naming the app
// it replaces), approval with body, and the claim. It returns the request,
// the approval and the claimed token.
func (e *env) pairOnce(admin member, name, replaces string, approve protocol.ApprovePairingRequest) (protocol.PairingCreated, protocol.PairingApproval, string) {
	e.t.Helper()
	rec := e.client("192.168.1.77").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{
		AppName: name, RequestedScopes: []string{"alerts:write"}, ReplacesAppID: replaces,
	})
	wantStatus(e.t, rec, http.StatusCreated, "")
	p := decodeBody[protocol.PairingCreated](e.t, rec)
	rec = admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", approve)
	wantStatus(e.t, rec, http.StatusOK, "")
	approval := decodeBody[protocol.PairingApproval](e.t, rec)
	rec = e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(e.t, rec, http.StatusOK, "")
	st := decodeBody[protocol.PairingStatus](e.t, rec)
	if st.Token == "" {
		e.t.Fatalf("claim = %s", rec.Body.String())
	}
	return p, approval, st.Token
}

func TestPairingHTTPReplace(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	_, first, oldToken := e.pairOnce(admin, "netdiag", "", protocol.ApprovePairingRequest{CreateChannel: true})
	old := bot{token: oldToken}
	wantStatus(t, old.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")

	// The re-pairing names the app; the admin listing shows what it replaces.
	p := e.createPairingReplacing("netdiag", first.App.ID)
	rec := admin.do(http.MethodGet, "/api/v1/admin/pairing-requests", nil)
	wantStatus(t, rec, http.StatusOK, "")
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || len(raw.Items) != 1 {
		t.Fatalf("listing = %s", rec.Body.String())
	}
	var replaces map[string]json.RawMessage
	if err := json.Unmarshal(raw.Items[0]["replaces"], &replaces); err != nil {
		t.Fatalf("replaces = %s: %v", raw.Items[0]["replaces"], err)
	}
	if keys := slices.Sorted(maps.Keys(replaces)); !slices.Equal(keys, []string{"app_id", "channels", "disabled_at", "name", "username"}) {
		t.Errorf("replaces keys = %v", keys)
	}
	list := decodeBody[protocol.PendingPairingList](t, rec)
	r := list.Items[0].Replaces
	if r == nil || r.AppID != first.App.ID || r.Name != "netdiag" || r.Username != "netdiag" || r.DisabledAt != nil ||
		len(r.Channels) != 1 || r.Channels[0].Name != "netdiag-alerts" {
		t.Fatalf("replaces = %+v", r)
	}
	if string(replaces["disabled_at"]) != "null" {
		t.Errorf("disabled_at = %s, want null", replaces["disabled_at"])
	}

	// replace_app reconnects the app: same id and bot, the alerts channel is reused.
	rec = admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", protocol.ApprovePairingRequest{ReplaceApp: true, CreateChannel: true})
	wantStatus(t, rec, http.StatusOK, "")
	approval := decodeBody[protocol.PairingApproval](t, rec)
	if approval.App.ID != first.App.ID || approval.App.UserID != first.App.UserID || !slices.Equal(approval.ChannelIDs, first.ChannelIDs) {
		t.Errorf("replace approval = %+v, first = %+v", approval, first)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps`); n != 1 {
		t.Errorf("apps after a replace = %d", n)
	}
	wantStatus(t, old.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")

	// The claim hands out the new token and ends the old one.
	rec = e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(t, rec, http.StatusOK, "")
	st := decodeBody[protocol.PairingStatus](t, rec)
	if st.State != "claimed" || !strings.HasPrefix(st.Token, "bub_") || len(st.Channels) != 1 {
		t.Fatalf("claim = %s", rec.Body.String())
	}
	wantStatus(t, old.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusUnauthorized, CodeUnauthorized)
	rec = bot{token: st.Token}.do(e, http.MethodGet, "/api/v1/apps/self", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if self := decodeBody[protocol.AppSelf](t, rec); self.App.ID != first.App.ID {
		t.Errorf("new token belongs to %s, want %s", self.App.ID, first.App.ID)
	}

	// replace_app on a request that names no app is a conflict.
	plain := e.createPairing("192.168.1.78", "other", "alerts:write")
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+plain.ID+"/approve", protocol.ApprovePairingRequest{ReplaceApp: true}), http.StatusConflict, CodeConflict)
	// Its listing entry has no replaces at all.
	rec = admin.do(http.MethodGet, "/api/v1/admin/pairing-requests", nil)
	var plainList struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &plainList); err != nil || len(plainList.Items) != 1 || plainList.Items[0]["replaces"] != nil {
		t.Errorf("listing of a request that names no app = %s", rec.Body.String())
	}
	// A malformed id is refused when the request is opened.
	c := e.client("192.168.1.79")
	ed := wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "x", ReplacesAppID: strings.Repeat("A", 65)}), http.StatusBadRequest, CodeBadRequest)
	if ed.FieldErrors()["replaces_app_id"] == "" {
		t.Errorf("no reason for replaces_app_id: %+v", ed)
	}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "x", ReplacesAppID: "not an id"}), http.StatusBadRequest, CodeBadRequest)
}

func (e *env) createPairingReplacing(name, appID string) protocol.PairingCreated {
	e.t.Helper()
	rec := e.client("192.168.1.80").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{
		AppName: name, RequestedScopes: []string{"alerts:write"}, ReplacesAppID: appID,
	})
	wantStatus(e.t, rec, http.StatusCreated, "")
	return decodeBody[protocol.PairingCreated](e.t, rec)
}

func TestAppsSelfRevoke(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	b := e.newBot(admin, "bot", []string{"alerts:write"}, nil)
	other := e.newBot(admin, "other", []string{"messages:read"}, nil)

	// A browser session cannot use it, nor can anyone without a token.
	wantStatus(t, admin.do(http.MethodDelete, "/api/v1/apps/self/token", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, alice.do(http.MethodDelete, "/api/v1/apps/self/token", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, e.client("").do(http.MethodDelete, "/api/v1/apps/self/token", nil), http.StatusUnauthorized, CodeUnauthorized)

	// Any scope may revoke itself; the next request with that token is 401.
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")
	rec := b.do(e, http.MethodDelete, "/api/v1/apps/self/token", nil)
	wantStatus(t, rec, http.StatusNoContent, "")
	if rec.Body.Len() != 0 {
		t.Errorf("204 body = %q", rec.Body.String())
	}
	wantStatus(t, b.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, b.do(e, http.MethodDelete, "/api/v1/apps/self/token", nil), http.StatusUnauthorized, CodeUnauthorized)
	// Only that token: another app is unaffected.
	wantStatus(t, other.do(e, http.MethodGet, "/api/v1/apps/self", nil), http.StatusOK, "")

	// The admin sees the revocation, audited with the app as the actor.
	list := decodeBody[protocol.AppList](t, admin.do(http.MethodGet, "/api/v1/admin/apps", nil))
	for _, a := range list.Items {
		if a.ID == b.app.ID && (len(a.Tokens) != 1 || a.Tokens[0].RevokedAt == nil) {
			t.Errorf("tokens of the bot = %+v", a.Tokens)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'token.revoke' AND actor_kind = 'app' AND actor_id = ? AND target_id = ?`,
		b.app.UserID, b.info.ID); n != 1 {
		t.Errorf("self-revoke audit rows = %d, want 1", n)
	}
}

func TestPairingHTTPCancel(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	cancel := func(p protocol.PairingCreated, secret string, set bool) int {
		t.Helper()
		rec := e.client("192.168.1.77").do(http.MethodDelete, "/api/v1/pairing/requests/"+p.ID, nil, func(r *http.Request) {
			if set {
				r.Header.Set(protocol.PairingHeader, secret)
			}
		})
		return rec.Code
	}

	p := e.createPairing("192.168.1.77", "netdiag", "alerts:write")
	// A missing or wrong secret, or an unknown id, is 404 like the poll.
	for _, tc := range []struct {
		secret string
		set    bool
	}{{"", false}, {"wrong", true}, {p.PollSecret + "x", true}} {
		if code := cancel(p, tc.secret, tc.set); code != http.StatusNotFound {
			t.Errorf("cancel with secret %q (set %v) = %d, want 404", tc.secret, tc.set, code)
		}
	}
	if code := cancel(protocol.PairingCreated{ID: "01JNOSUCHREQUEST000000000"}, p.PollSecret, true); code != http.StatusNotFound {
		t.Errorf("cancel of an unknown id = %d", code)
	}
	if code := cancel(p, p.PollSecret, true); code != http.StatusNoContent {
		t.Fatalf("cancel = %d", code)
	}
	rec := e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(t, rec, http.StatusOK, "")
	raw := decodeBody[map[string]json.RawMessage](t, rec)
	if string(raw["state"]) != `"cancelled"` || raw["token"] != nil {
		t.Errorf("poll of a cancelled request = %s", rec.Body.String())
	}
	if code := cancel(p, p.PollSecret, true); code != http.StatusNoContent {
		t.Errorf("second cancel = %d, want 204", code)
	}
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", nil), http.StatusConflict, CodeConflict)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/deny", nil), http.StatusConflict, CodeConflict)

	// An approved request is cancelled too, and its new app disabled.
	q := e.createPairing("192.168.1.78", "lab", "alerts:write")
	rec = admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+q.ID+"/approve", nil)
	wantStatus(t, rec, http.StatusOK, "")
	app := decodeBody[protocol.PairingApproval](t, rec).App
	if code := cancel(q, q.PollSecret, true); code != http.StatusNoContent {
		t.Fatalf("cancel of an approval = %d", code)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NOT NULL`, app.ID); n != 1 {
		t.Error("the app of a cancelled approval is enabled")
	}

	// A claimed request cannot be cancelled.
	claimed, _, _ := e.pairOnce(admin, "claimed", "", protocol.ApprovePairingRequest{})
	rec = e.client("192.168.1.77").do(http.MethodDelete, "/api/v1/pairing/requests/"+claimed.ID, nil, func(r *http.Request) {
		r.Header.Set(protocol.PairingHeader, claimed.PollSecret)
	})
	ed := wantStatus(t, rec, http.StatusConflict, CodeConflict)
	if ed.Message != "this pairing request is claimed" {
		t.Errorf("conflict message = %q", ed.Message)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.cancel' AND actor_kind = 'system'`); n != 2 {
		t.Errorf("pairing.cancel audit rows = %d, want 2", n)
	}

	// The network gate and the Origin check apply like for the poll.
	wantStatus(t, e.client("8.8.8.8").do(http.MethodDelete, "/api/v1/pairing/requests/"+p.ID, nil), http.StatusForbidden, CodeForbidden)
}
