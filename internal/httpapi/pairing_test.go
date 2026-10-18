package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/apps"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (e *env) createPairing(ip, name string, scopes ...string) protocol.PairingCreated {
	e.t.Helper()
	rec := e.client(ip).do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{
		AppName: name, Description: "sends alerts", RequestedScopes: scopes,
	})
	wantStatus(e.t, rec, http.StatusCreated, "")
	return decodeBody[protocol.PairingCreated](e.t, rec)
}

func (e *env) pollPairing(ip string, p protocol.PairingCreated, secret string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.client(ip).do(http.MethodGet, "/api/v1/pairing/requests/"+p.ID, nil, func(r *http.Request) {
		r.Header.Set(protocol.PairingHeader, secret)
	})
}

func TestPairingHTTPFlow(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ops := admin.createChannel(t, "ops", "public")

	p := e.createPairing("192.168.1.77", "netdiag", "alerts:write")
	if !regexp.MustCompile(`^\d{6}$`).MatchString(p.Code) || p.PollSecret == "" || p.ID == "" {
		t.Fatalf("created = %+v", p)
	}
	if want := protocol.FormatTime(e.clock.Now().Add(apps.PairingTTL).UnixMilli()); p.ExpiresAt != want {
		t.Errorf("expires_at = %s, want %s", p.ExpiresAt, want)
	}
	rec := e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(t, rec, http.StatusOK, "")
	if st := decodeBody[protocol.PairingStatus](t, rec); st.State != "pending" || st.Token != "" {
		t.Fatalf("poll before approval = %+v", st)
	}

	// The admin sees the code, where the request came from and the CA fingerprint.
	rec = admin.do(http.MethodGet, "/api/v1/admin/pairing-requests", nil)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeBody[protocol.PendingPairingList](t, rec)
	if len(list.Items) != 1 {
		t.Fatalf("pending = %+v", list)
	}
	got := list.Items[0]
	if got.ID != p.ID || got.Code != p.Code || got.SourceIP != "192.168.1.77" || got.CAFingerprint != testEndpoint.Fingerprint ||
		got.AppName != "netdiag" || !slices.Equal(got.RequestedScopes, []string{"alerts:write"}) {
		t.Errorf("pending[0] = %+v", got)
	}
	if strings.Contains(rec.Body.String(), p.PollSecret) {
		t.Error("the poll secret is in the admin listing")
	}

	// Approval: scopes and channels chosen by the admin, plus a new alerts channel.
	rec = admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", protocol.ApprovePairingRequest{
		Scopes: []string{"alerts:write", "messages:write"}, ChannelIDs: []string{ops.ID}, CreateChannel: true,
	})
	wantStatus(t, rec, http.StatusOK, "")
	approval := decodeBody[protocol.PairingApproval](t, rec)
	if approval.App.Name != "netdiag" || len(approval.ChannelIDs) != 2 || approval.ChannelIDs[1] != ops.ID {
		t.Fatalf("approval = %+v", approval)
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE name = 'netdiag-alerts' AND kind = 'public'`); n != 1 {
		t.Error("alerts channel not created")
	}
	for _, ch := range approval.ChannelIDs {
		if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, ch, approval.App.UserID); n != 1 {
			t.Errorf("bot not in channel %s", ch)
		}
	}
	if list := decodeBody[protocol.PendingPairingList](t, admin.do(http.MethodGet, "/api/v1/admin/pairing-requests", nil)); len(list.Items) != 0 {
		t.Errorf("approved request still pending: %+v", list.Items)
	}

	// Only the right poll secret gets the token, and only once.
	for _, secret := range []string{"", "wrong", p.PollSecret + "x"} {
		wantStatus(t, e.pollPairing("192.168.1.77", p, secret), http.StatusNotFound, CodeNotFound)
	}
	unknown := protocol.PairingCreated{ID: "01JNOSUCHREQUEST000000000", PollSecret: p.PollSecret}
	wantStatus(t, e.pollPairing("192.168.1.77", unknown, unknown.PollSecret), http.StatusNotFound, CodeNotFound)
	rec = e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(t, rec, http.StatusOK, "")
	st := decodeBody[protocol.PairingStatus](t, rec)
	if st.State != "claimed" || !strings.HasPrefix(st.Token, "bub_") {
		t.Fatalf("claim = %+v", st)
	}
	// The claim answer lists the bot's channels by id and name, sorted by name.
	raw := decodeBody[map[string]json.RawMessage](t, rec)
	if keys := slices.Sorted(maps.Keys(raw)); !slices.Equal(keys, []string{"channels", "expires_at", "state", "token"}) {
		t.Errorf("claim keys = %v", keys)
	}
	var chans []map[string]string
	if err := json.Unmarshal(raw["channels"], &chans); err != nil {
		t.Fatalf("channels %s: %v", raw["channels"], err)
	}
	want := []map[string]string{{"id": approval.ChannelIDs[0], "name": "netdiag-alerts"}, {"id": ops.ID, "name": "ops"}}
	if !slices.EqualFunc(chans, want, maps.Equal) {
		t.Errorf("claim channels = %s, want %v", raw["channels"], want)
	}
	for range 3 {
		rec = e.pollPairing("192.168.1.77", p, p.PollSecret)
		wantStatus(t, rec, http.StatusOK, "")
		if again := decodeBody[protocol.PairingStatus](t, rec); again.State != "claimed" || again.Token != "" || strings.Contains(rec.Body.String(), st.Token) ||
			strings.Contains(rec.Body.String(), "channels") {
			t.Fatalf("second claim = %s", rec.Body.String())
		}
	}

	// The claimed token works with exactly the approved scopes.
	b := bot{token: st.Token}
	rec = b.do(e, http.MethodGet, "/api/v1/apps/self", nil)
	wantStatus(t, rec, http.StatusOK, "")
	self := decodeBody[protocol.AppSelf](t, rec)
	if self.App.ID != approval.App.ID || !slices.Equal(self.Token.Scopes, []string{"messages:write", "alerts:write"}) || self.Token.ChannelIDs != nil {
		t.Errorf("self = %+v", self)
	}
	wantStatus(t, b.do(e, http.MethodPost, "/api/v1/alerts", alertIn(func(in *protocol.AlertInput) { in.Channel = "ops" })), http.StatusCreated, "")

	// Approving again, or denying, is a conflict; the approval is audited once.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", nil), http.StatusConflict, CodeConflict)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/deny", nil), http.StatusConflict, CodeConflict)
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.approve' AND actor_id = ?`, admin.id); n != 1 {
		t.Errorf("pairing.approve audit entries = %d", n)
	}
}

func TestPairingHTTPDenyAndExpiry(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()

	denied := e.createPairing("192.168.1.77", "denied")
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+denied.ID+"/deny", nil), http.StatusNoContent, "")
	if st := decodeBody[protocol.PairingStatus](t, e.pollPairing("192.168.1.77", denied, denied.PollSecret)); st.State != "denied" || st.Token != "" {
		t.Errorf("denied poll = %+v", st)
	}
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+denied.ID+"/approve", protocol.ApprovePairingRequest{Scopes: []string{"mcp"}}), http.StatusConflict, CodeConflict)
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.deny'`); n != 1 {
		t.Errorf("pairing.deny audit entries = %d", n)
	}

	late := e.createPairing("192.168.1.78", "late", "alerts:write")
	e.clock.Advance(apps.PairingTTL)
	rec := e.pollPairing("192.168.1.78", late, late.PollSecret)
	wantStatus(t, rec, http.StatusOK, "")
	if st := decodeBody[protocol.PairingStatus](t, rec); st.State != "expired" || st.Token != "" {
		t.Errorf("expired poll = %+v", st)
	}
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+late.ID+"/approve", nil), http.StatusConflict, CodeConflict)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/nope/approve", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/nope/deny", nil), http.StatusNotFound, CodeNotFound)
	if list := decodeBody[protocol.PendingPairingList](t, admin.do(http.MethodGet, "/api/v1/admin/pairing-requests", nil)); len(list.Items) != 0 || list.Items == nil {
		t.Errorf("pending = %+v", list.Items)
	}
}

func TestPairingHTTPApprovalValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	p := e.createPairing("192.168.1.77", "netdiag", "alerts:write")

	wantStatus(t, alice.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", nil), http.StatusForbidden, CodeForbidden)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", protocol.ApprovePairingRequest{Scopes: []string{"root"}}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", protocol.ApprovePairingRequest{ChannelIDs: []string{"nope"}}), http.StatusBadRequest, CodeBadRequest)
	// A private channel that already has the name of the alerts channel fails the whole approval.
	admin.createChannel(t, "netdiag-alerts", "private")
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", protocol.ApprovePairingRequest{CreateChannel: true}), http.StatusConflict, CodeConflict)
	if n := e.count(`SELECT COUNT(*) FROM apps`); n != 0 {
		t.Errorf("failed approvals left %d apps", n)
	}
	// With no body the requested scopes are granted, and the bot joins no
	// channel: the claim answer then has no channels at all.
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/pairing-requests/"+p.ID+"/approve", nil), http.StatusOK, "")
	claim := e.pollPairing("192.168.1.77", p, p.PollSecret)
	wantStatus(t, claim, http.StatusOK, "")
	if raw := decodeBody[map[string]json.RawMessage](t, claim); raw["token"] == nil || raw["channels"] != nil {
		t.Errorf("claim of a bot in no channel = %s", claim.Body.String())
	}
	st := decodeBody[protocol.PairingStatus](t, claim)
	rec := bot{token: st.Token}.do(e, http.MethodGet, "/api/v1/apps/self", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if self := decodeBody[protocol.AppSelf](t, rec); !slices.Equal(self.Token.Scopes, []string{"alerts:write"}) {
		t.Errorf("default scopes = %v", self.Token.Scopes)
	}
}

func TestPairingHTTPLimits(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.createPairing("192.168.1.77", "app"+strconv.Itoa(i))
	}
	rec := e.client("192.168.1.77").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "sixth"})
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if ra, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || ra < 1 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	// Other addresses are separate, up to 20 pending requests overall.
	for i := 5; i < apps.MaxPendingPairings; i++ {
		e.createPairing("192.168.2."+strconv.Itoa(i), "app"+strconv.Itoa(i))
	}
	rec = e.client("192.168.3.1").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "twenty-first"})
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests WHERE state = 'pending'`); n != apps.MaxPendingPairings {
		t.Errorf("pending = %d", n)
	}
	// Room again after the requests expire (and the address bucket refilled).
	e.clock.Advance(apps.PairingTTL)
	wantStatus(t, e.client("192.168.1.77").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "again"}), http.StatusCreated, "")
}

func TestPairingHTTPNetworkGateAndValidation(t *testing.T) {
	e := newEnv(t)
	// The LAN guard applies like everywhere: a public address never reaches pairing.
	wantStatus(t, e.client("8.8.8.8").do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "x"}), http.StatusForbidden, CodeForbidden)
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests`); n != 0 {
		t.Errorf("%d requests stored from a public address", n)
	}
	c := e.client("192.168.1.77")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{}), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: strings.Repeat("n", 65)}), http.StatusBadRequest, CodeBadRequest)
	ed := wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "x", RequestedScopes: []string{"god"}}), http.StatusBadRequest, CodeBadRequest)
	if ed.FieldErrors()["requested_scopes"] == "" {
		t.Errorf("no reason for requested_scopes: %+v", ed)
	}
	wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", `{"app_name":"x","extra":1}`), http.StatusBadRequest, CodeBadRequest)
	// A browser on another origin cannot be made to open pairing requests.
	wantStatus(t, c.do(http.MethodPost, "/api/v1/pairing/requests", protocol.PairingRequestInput{AppName: "x"}, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example")
	}), http.StatusForbidden, CodeForbidden)
}
