package apps

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// repair opens a pairing request from an app that names the app it was
// paired as before.
func (e *env) repair(name, replaces string, scopes ...string) protocol.PairingCreated {
	e.t.Helper()
	p, err := e.svc.CreatePairing(e.ctx, "192.168.1.9", protocol.PairingRequestInput{
		AppName: name, Description: "sends alerts again", RequestedScopes: scopes, ReplacesAppID: replaces,
	})
	if err != nil {
		e.t.Fatalf("create pairing: %v", err)
	}
	return p
}

// pairedApp pairs a new app "netdiag" whose bot is in the given channels and
// returns the approval and the claimed token.
func (e *env) pairedApp(channelIDs ...string) (protocol.PairingApproval, string) {
	e.t.Helper()
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ChannelIDs: channelIDs})
	if err != nil {
		e.t.Fatal(err)
	}
	st := e.poll(p)
	if st.Token == "" {
		e.t.Fatalf("claim = %+v", st)
	}
	return res, st.Token
}

// pending returns the listing entry of pairing request id.
func (e *env) pending(id string) protocol.PendingPairing {
	e.t.Helper()
	list, err := e.svc.ListPending(e.ctx, "")
	if err != nil {
		e.t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == id {
			return p
		}
	}
	e.t.Fatalf("pairing request %s not pending", id)
	return protocol.PendingPairing{}
}

func (e *env) live(token string) bool {
	e.t.Helper()
	pr, err := e.authenticate("Bearer " + token)
	if err != nil {
		e.t.Fatal(err)
	}
	return pr != nil
}

func TestPairingReplaceKeepsTheAppAndRevokesOldTokensAtTheClaim(t *testing.T) {
	e := newEnv(t)
	ops := e.channel("ops", protocol.ChannelPublic)
	lab := e.channel("lab", protocol.ChannelPublic)
	first, oldToken := e.pairedApp(ops.ID)
	app := first.App
	extra := e.token(app.ID, "messages:read")
	if !e.live(oldToken) || !e.live(extra.Secret) {
		t.Fatal("tokens of the first pairing do not work")
	}

	p := e.repair("netdiag", app.ID, "alerts:write", "messages:write")
	got := e.pending(p.ID)
	want := protocol.PairingReplaces{AppID: app.ID, Name: "netdiag", Username: "netdiag", Channels: []protocol.PairingChannel{{ID: ops.ID, Name: "ops"}}}
	if got.Replaces == nil || got.Replaces.AppID != want.AppID || got.Replaces.Name != want.Name || got.Replaces.Username != want.Username ||
		got.Replaces.DisabledAt != nil || !slices.Equal(got.Replaces.Channels, want.Channels) {
		t.Fatalf("replaces = %+v, want %+v", got.Replaces, want)
	}

	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{
		ReplaceApp: true, ChannelIDs: []string{lab.ID, ops.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// No app or bot was created: the existing one keeps its id, bot and
	// username, and takes the request's description.
	if res.App.ID != app.ID || res.App.UserID != app.UserID || res.App.Username != "netdiag" || res.App.Description != "sends alerts again" {
		t.Errorf("approval app = %+v, want the existing %+v", res.App, app)
	}
	if !slices.Equal(res.ChannelIDs, []string{lab.ID, ops.ID}) {
		t.Errorf("approval channel_ids = %v", res.ChannelIDs)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM apps`:                     1,
		`SELECT COUNT(*) FROM users WHERE kind = 'app'`: 1,
		`SELECT COUNT(*) FROM pairing_requests WHERE replaced = 1 AND state = 'approved' AND approved_app_id = '` + app.ID + `'`: 1,
		`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.approve' AND json_extract(detail, '$.replaced_app') = 1`:         1,
		`SELECT COUNT(*) FROM audit_log WHERE action = 'app.create'`:                                                             1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	// Nothing is revoked before the claim: an approval that is never claimed
	// leaves the app working.
	if !e.live(oldToken) || !e.live(extra.Secret) {
		t.Fatal("old tokens stopped working before the claim")
	}
	if _, ok := e.svc.touched[extra.Info.ID]; !ok {
		t.Fatal("the authenticator did not remember the old token")
	}

	st := e.poll(p)
	if st.State != protocol.PairingClaimed || !strings.HasPrefix(st.Token, "bub_") {
		t.Fatalf("claim = %+v", st)
	}
	want2 := []protocol.PairingChannel{{ID: lab.ID, Name: "lab"}, {ID: ops.ID, Name: "ops"}}
	if !slices.Equal(st.Channels, want2) {
		t.Errorf("claim channels = %+v, want %+v", st.Channels, want2)
	}
	pr, err := e.authenticate("Bearer " + st.Token)
	if err != nil || pr == nil || pr.Token.AppID != app.ID || pr.User.ID != app.UserID ||
		!slices.Equal(pr.Token.Scopes, []string{"messages:write", "alerts:write"}) {
		t.Fatalf("new token = %+v, %v", pr, err)
	}
	if e.live(oldToken) || e.live(extra.Secret) {
		t.Error("an old token still works after the claim")
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM app_tokens WHERE app_id = '` + app.ID + `' AND revoked_at IS NOT NULL`:                         2,
		`SELECT COUNT(*) FROM app_tokens WHERE app_id = '` + app.ID + `' AND revoked_at IS NULL`:                             1,
		`SELECT COUNT(*) FROM audit_log WHERE action = 'token.revoke' AND actor_kind = 'system' AND target_id != ''`:         2,
		`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND payload LIKE '%"token_id":"` + extra.Info.ID + `"%'`: 1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	if _, ok := e.svc.touched[extra.Info.ID]; ok {
		t.Error("the revoked token is still in the authenticator's cache")
	}
}

func TestPairingReplaceUnclaimedExpiryKeepsTheAppEnabled(t *testing.T) {
	e := newEnv(t)
	first, oldToken := e.pairedApp()
	p := e.repair("netdiag", first.App.ID, "alerts:write")
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(PairingTTL + ClaimWindow)
	if st := e.poll(p); st.State != protocol.PairingExpired || st.Token != "" {
		t.Fatalf("unclaimed replace after the window = %+v", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NULL`, first.App.ID); n != 1 {
		t.Error("the expired replace disabled the existing app")
	}
	if !e.live(oldToken) {
		t.Error("the old token stopped working after an unclaimed replace expired")
	}
}

// disabledApp pairs "netdiag", gives it a second token, disables it (as an
// admin who suspects a leaked token would) and returns the app and both
// token secrets, with the second token in the authenticator's cache.
func (e *env) disabledApp() (protocol.App, IssuedToken, string) {
	e.t.Helper()
	first, oldToken := e.pairedApp()
	extra := e.token(first.App.ID, "messages:read")
	if !e.live(extra.Secret) {
		e.t.Fatal("the second token does not work")
	}
	off := true
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, first.App.ID, UpdateAppInput{Disabled: &off}); err != nil {
		e.t.Fatal(err)
	}
	if e.live(oldToken) || e.live(extra.Secret) {
		e.t.Fatal("a token of the disabled app works")
	}
	return first.App, extra, oldToken
}

func TestPairingReplaceReenablesADisabledApp(t *testing.T) {
	e := newEnv(t)
	app, extra, oldToken := e.disabledApp()
	p := e.repair("netdiag", app.ID, "alerts:write")
	if r := e.pending(p.ID).Replaces; r == nil || r.DisabledAt == nil {
		t.Fatalf("replaces of a disabled app = %+v", r)
	}
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ReplaceApp: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.App.DisabledAt != nil {
		t.Errorf("reconnected app is still disabled: %+v", res.App)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'app.enable' AND target_id = ? AND actor_id = ? AND json_extract(detail, '$.pairing_request') = ?`,
		app.ID, e.admin.ID, p.ID); n != 1 {
		t.Errorf("app.enable audit rows = %d, want 1", n)
	}
	// The approval revokes the disabled app's tokens itself, so enabling it
	// revives none of them, between the approval and the claim or ever.
	if e.live(oldToken) || e.live(extra.Secret) {
		t.Error("enabling the reconnected app revived an old token")
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM app_tokens WHERE app_id = '` + app.ID + `' AND revoked_at IS NULL`: 0,
		`SELECT COUNT(*) FROM audit_log WHERE action = 'token.revoke' AND actor_id = '` + e.admin.ID +
			`' AND json_extract(detail, '$.pairing_request') = '` + p.ID + `'`: 2,
		`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND payload LIKE '%"token_id":"` + extra.Info.ID + `"%'`: 1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	if _, ok := e.svc.touched[extra.Info.ID]; ok {
		t.Error("a token revoked by the approval is still in the authenticator's cache")
	}
	// The claim then has nothing left to revoke.
	st := e.poll(p)
	if st.State != protocol.PairingClaimed || !e.live(st.Token) || e.live(oldToken) || e.live(extra.Secret) {
		t.Errorf("claim = %+v (new live %v, old live %v)", st, e.live(st.Token), e.live(oldToken))
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'token.revoke' AND actor_kind = 'system'`); n != 0 {
		t.Errorf("the claim revoked %d more tokens, want 0", n)
	}
}

func TestPairingReplaceOfADisabledAppNeverClaimedRevivesNoToken(t *testing.T) {
	for _, end := range []string{"expiry", "cancel"} {
		t.Run(end, func(t *testing.T) {
			e := newEnv(t)
			app, extra, oldToken := e.disabledApp()
			p := e.repair("netdiag", app.ID, "alerts:write")
			if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); err != nil {
				t.Fatal(err)
			}
			want := protocol.PairingExpired
			if end == "cancel" {
				if err := e.cancel(p); err != nil {
					t.Fatal(err)
				}
				want = protocol.PairingCancelled
			} else {
				e.clock.Advance(PairingTTL + ClaimWindow)
			}
			if st := e.poll(p); st.State != want || st.Token != "" {
				t.Fatalf("poll after the %s = %+v", end, st)
			}
			// The app stays enabled, as for any replace, but none of its old
			// tokens works again.
			if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NULL`, app.ID); n != 1 {
				t.Error("the app was disabled again")
			}
			if e.live(oldToken) || e.live(extra.Secret) {
				t.Errorf("an old token of the disabled app works after the %s", end)
			}
		})
	}
}

func TestPairingReplaceNeedsAnExistingApp(t *testing.T) {
	e := newEnv(t)
	// A request that names no app cannot be approved as a replace.
	plain := e.pair("192.168.1.9", "netdiag", "alerts:write")
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, plain.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); !errors.Is(err, chat.ErrConflict) ||
		!strings.Contains(err.Error(), "does not name an existing app") {
		t.Errorf("replace without a named app: %v", err)
	}
	// An id that names no app is accepted and ignored.
	ghost := e.repair("netdiag", "01JNOSUCHAPP00000000000000", "alerts:write")
	if r := e.pending(ghost.ID).Replaces; r != nil {
		t.Errorf("replaces of an unknown app = %+v", r)
	}
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests WHERE id = ? AND replaces_app_id IS NULL`, ghost.ID); n != 1 {
		t.Error("an unknown app id was stored")
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, ghost.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("replace of an unknown app: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps`); n != 0 {
		t.Errorf("failed replace approvals created %d apps", n)
	}
	// Approving it as a new app still works.
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, ghost.ID, protocol.ApprovePairingRequest{})
	if err != nil || res.App.Username != "netdiag" {
		t.Fatalf("new-app approval = %+v, %v", res, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests WHERE id = ? AND replaced = 0`, ghost.ID); n != 1 {
		t.Error("a new-app approval is marked replaced")
	}
	// The id is validated: at most 64 letters, digits, '-' or '_'.
	for _, bad := range []string{strings.Repeat("A", 65), "01J'; DROP", "a b", "é"} {
		_, err := e.svc.CreatePairing(e.ctx, "192.168.1.10", protocol.PairingRequestInput{AppName: "x", ReplacesAppID: bad})
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields["replaces_app_id"] == "" {
			t.Errorf("replaces_app_id %q: %v, want a validation error", bad, err)
		}
	}
	if _, err := e.svc.CreatePairing(e.ctx, "192.168.1.10", protocol.PairingRequestInput{AppName: "x", ReplacesAppID: strings.Repeat("A", 64)}); err != nil {
		t.Errorf("a 64-character id: %v", err)
	}
}

func TestPairingReplaceOfADeactivatedBotIsAConflict(t *testing.T) {
	e := newEnv(t)
	first, _ := e.pairedApp()
	err := e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE users SET deactivated_at = ? WHERE id = ?`, tx.Now(), first.App.UserID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	p := e.repair("netdiag", first.App.ID, "alerts:write")
	// Such an app cannot be reconnected, so the listing shows the request as
	// a new app instead of offering a reconnect that could only fail.
	if r := e.pending(p.ID).Replaces; r != nil {
		t.Errorf("replaces of an app with a deactivated bot = %+v, want none", r)
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); !errors.Is(err, chat.ErrConflict) ||
		!strings.Contains(err.Error(), "deactivated") {
		t.Errorf("replace of a deactivated bot: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND description = ?`, first.App.ID, first.App.Description); n != 1 {
		t.Error("the refused replace changed the app")
	}
}

func TestPairingAlertsChannelIsReusedWhenPublic(t *testing.T) {
	e := newEnv(t)
	existing := e.channel("netdiag-alerts", protocol.ChannelPublic)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{CreateChannel: true, ChannelIDs: []string{existing.ID}})
	if err != nil {
		t.Fatalf("approval with an existing public alerts channel: %v", err)
	}
	if !slices.Equal(res.ChannelIDs, []string{existing.ID}) {
		t.Errorf("channel_ids = %v, want only the existing channel", res.ChannelIDs)
	}
	if n := e.count(`SELECT COUNT(*) FROM channels WHERE name = 'netdiag-alerts'`); n != 1 {
		t.Errorf("channels named netdiag-alerts = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, existing.ID, res.App.UserID); n != 1 {
		t.Error("bot not added to the existing alerts channel")
	}
	st := e.poll(p)

	// A replace whose bot is already in it: adding it again changes nothing.
	again := e.repair("netdiag", res.App.ID, "alerts:write")
	res2, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, again.ID, protocol.ApprovePairingRequest{ReplaceApp: true, CreateChannel: true})
	if err != nil || !slices.Equal(res2.ChannelIDs, []string{existing.ID}) {
		t.Fatalf("replace approval = %+v, %v", res2, err)
	}
	if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, existing.ID, res.App.UserID); n != 1 {
		t.Errorf("memberships of the bot = %d", n)
	}
	if !e.live(st.Token) {
		t.Error("the token of the first pairing stopped working before the second claim")
	}
}

func TestPairingAlertsChannelPrivateOrArchivedIsAConflict(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		archive    bool
		msg        string
	}{
		{"private", protocol.ChannelPrivate, false, "private channel"},
		{"archived", protocol.ChannelPublic, true, "archived channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ch := e.channel("netdiag-alerts", tc.kind)
			if tc.archive {
				if _, err := e.chat.ArchiveChannel(e.ctx, e.admin, ch.ID); err != nil {
					t.Fatal(err)
				}
			}
			p := e.pair("192.168.1.9", "netdiag", "alerts:write")
			_, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{CreateChannel: true})
			if !errors.Is(err, chat.ErrConflict) || !strings.Contains(err.Error(), tc.msg) || !strings.Contains(err.Error(), "#netdiag-alerts") {
				t.Fatalf("approval = %v, want a conflict about the %s", err, tc.msg)
			}
			if n := e.count(`SELECT COUNT(*) FROM apps`); n != 0 {
				t.Errorf("failed approval left %d apps", n)
			}
			if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id != ?`, ch.ID, e.admin.ID); n != 0 {
				t.Errorf("someone was added to the %s channel", tc.name)
			}
		})
	}
}

func (e *env) cancel(p protocol.PairingCreated) error {
	return e.svc.CancelPairing(e.ctx, "192.168.1.9", p.ID, p.PollSecret)
}

func TestPairingCancelPending(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	for _, secret := range []string{"", "wrong", p.PollSecret + "x"} {
		if err := e.svc.CancelPairing(e.ctx, "192.168.1.9", p.ID, secret); !errors.Is(err, chat.ErrNotFound) {
			t.Errorf("cancel with secret %q: %v, want not found", secret, err)
		}
	}
	if err := e.svc.CancelPairing(e.ctx, "192.168.1.9", "01JNOSUCHREQUEST000000000", p.PollSecret); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("cancel of an unknown id: %v", err)
	}
	if st := e.poll(p); st.State != protocol.PairingPending {
		t.Fatalf("wrong secrets changed the request: %+v", st)
	}
	if err := e.cancel(p); err != nil {
		t.Fatal(err)
	}
	if st := e.poll(p); st.State != protocol.PairingCancelled || st.Token != "" {
		t.Errorf("poll of a cancelled request = %+v", st)
	}
	if list, _ := e.svc.ListPending(e.ctx, ""); len(list) != 0 {
		t.Errorf("cancelled request still listed: %+v", list)
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{}); !errors.Is(err, chat.ErrConflict) ||
		!strings.Contains(err.Error(), "cancelled") {
		t.Errorf("approving a cancelled request: %v", err)
	}
	if err := e.svc.DenyPairing(e.ctx, e.aud, p.ID); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("denying a cancelled request: %v", err)
	}
	// Cancelling again is fine and records nothing new.
	if err := e.cancel(p); err != nil {
		t.Errorf("second cancel: %v", err)
	}
	var detail string
	if err := e.db.Read().QueryRow(`SELECT detail FROM audit_log WHERE action = 'pairing.cancel' AND actor_kind = 'system' AND target_id = ?`, p.ID).Scan(&detail); err != nil {
		t.Fatalf("pairing.cancel audit: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(detail), &d); err != nil || d["app_name"] != "netdiag" || d["state"] != "pending" {
		t.Errorf("audit detail = %s", detail)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.cancel'`); n != 1 {
		t.Errorf("pairing.cancel audit rows = %d, want 1", n)
	}
}

func TestPairingCancelApprovedDisablesTheNewApp(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.cancel(p); err != nil {
		t.Fatal(err)
	}
	if st := e.poll(p); st.State != protocol.PairingCancelled || st.Token != "" {
		t.Errorf("poll after cancelling an approval = %+v", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NOT NULL`, res.App.ID); n != 1 {
		t.Error("the app created for a cancelled approval is still enabled")
	}
	if n := e.count(`SELECT COUNT(*) FROM app_tokens`); n != 0 {
		t.Errorf("a cancelled approval minted %d tokens", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.cancel' AND json_extract(detail, '$.app_disabled') = 1 AND json_extract(detail, '$.app_id') = ?`, res.App.ID); n != 1 {
		t.Errorf("pairing.cancel audit of the approval = %d rows", n)
	}
}

func TestPairingCancelApprovedReplaceLeavesTheApp(t *testing.T) {
	e := newEnv(t)
	first, oldToken := e.pairedApp()
	p := e.repair("netdiag", first.App.ID, "alerts:write")
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ReplaceApp: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.cancel(p); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NULL`, first.App.ID); n != 1 {
		t.Error("cancelling a replace disabled the existing app")
	}
	if !e.live(oldToken) {
		t.Error("cancelling a replace revoked the old token")
	}
	if st := e.poll(p); st.State != protocol.PairingCancelled || st.Token != "" {
		t.Errorf("poll = %+v", st)
	}
}

func TestPairingCancelTerminalStatesAreConflicts(t *testing.T) {
	e := newEnv(t)
	claimed := e.pair("192.168.1.9", "claimed", "alerts:write")
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, claimed.ID, protocol.ApprovePairingRequest{}); err != nil {
		t.Fatal(err)
	}
	st := e.poll(claimed)
	denied := e.pair("192.168.1.9", "denied")
	if err := e.svc.DenyPairing(e.ctx, e.aud, denied.ID); err != nil {
		t.Fatal(err)
	}
	expired := e.pair("192.168.1.9", "expired")
	e.clock.Advance(PairingTTL)
	for _, tc := range []struct {
		p     protocol.PairingCreated
		state string
	}{{claimed, "claimed"}, {denied, "denied"}, {expired, "expired"}} {
		err := e.cancel(tc.p)
		if !errors.Is(err, chat.ErrConflict) || err.Error() != "this pairing request is "+tc.state {
			t.Errorf("cancel of a %s request: %v", tc.state, err)
		}
	}
	if !e.live(st.Token) {
		t.Error("cancelling a claimed request touched its token")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.cancel'`); n != 0 {
		t.Errorf("refused cancels wrote %d audit rows", n)
	}
}

func TestPairingCancelSharesThePollLimit(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag")
	for range PairingPollBurst {
		if _, err := e.svc.PollPairing(e.ctx, "192.168.1.77", p.ID, p.PollSecret); err != nil {
			t.Fatal(err)
		}
	}
	var rl *auth.RateLimitedError
	if err := e.svc.CancelPairing(e.ctx, "192.168.1.77", p.ID, p.PollSecret); !errors.As(err, &rl) {
		t.Fatalf("cancel after the poll burst: %v, want rate limited", err)
	}
	e.clock.Advance(time.Second)
	if err := e.svc.CancelPairing(e.ctx, "192.168.1.77", p.ID, p.PollSecret); err != nil {
		t.Errorf("cancel after the bucket refilled: %v", err)
	}
}
