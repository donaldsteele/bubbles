package apps

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func (e *env) pair(ip, name string, scopes ...string) protocol.PairingCreated {
	e.t.Helper()
	p, err := e.svc.CreatePairing(e.ctx, ip, protocol.PairingRequestInput{AppName: name, Description: "sends alerts", RequestedScopes: scopes})
	if err != nil {
		e.t.Fatalf("create pairing: %v", err)
	}
	return p
}

func (e *env) poll(p protocol.PairingCreated) protocol.PairingStatus {
	e.t.Helper()
	st, err := e.svc.PollPairing(e.ctx, "192.168.1.9", p.ID, p.PollSecret)
	if err != nil {
		e.t.Fatalf("poll pairing: %v", err)
	}
	return st
}

func TestPairingRequestShape(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write", "alerts:write")
	if !regexp.MustCompile(`^\d{6}$`).MatchString(p.Code) {
		t.Errorf("code = %q", p.Code)
	}
	if p.ID == "" || len(p.PollSecret) != 52 {
		t.Errorf("created = %+v", p)
	}
	if want := protocol.FormatTime(e.clock.Now().Add(PairingTTL).UnixMilli()); p.ExpiresAt != want {
		t.Errorf("expires_at = %s, want %s", p.ExpiresAt, want)
	}
	var hash []byte
	var ip string
	var scopes string
	if err := e.db.Read().QueryRow(`SELECT poll_secret_hash, source_ip, requested_scopes FROM pairing_requests WHERE id = ?`, p.ID).Scan(&hash, &ip, &scopes); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, auth.HashToken(p.PollSecret)) || ip != "192.168.1.9" || scopes != `["alerts:write"]` {
		t.Errorf("stored hash ok=%v ip=%q scopes=%s", bytes.Equal(hash, auth.HashToken(p.PollSecret)), ip, scopes)
	}
	pending, err := e.svc.ListPending(e.ctx, "AB12 CD34")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	got := pending[0]
	if got.Code != p.Code || got.SourceIP != "192.168.1.9" || got.CAFingerprint != "AB12 CD34" || got.AppName != "netdiag" ||
		!slices.Equal(got.RequestedScopes, []string{"alerts:write"}) {
		t.Errorf("pending[0] = %+v", got)
	}
	if st := e.poll(p); st.State != protocol.PairingPending || st.Token != "" {
		t.Errorf("poll before approval = %+v", st)
	}
}

func TestPairingRequestValidation(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name  string
		in    protocol.PairingRequestInput
		field string
	}{
		{"no name", protocol.PairingRequestInput{}, "app_name"},
		{"long name", protocol.PairingRequestInput{AppName: strings.Repeat("n", 65)}, "app_name"},
		{"unknown scope", protocol.PairingRequestInput{AppName: "x", RequestedScopes: []string{"god"}}, "requested_scopes"},
		{"long description", protocol.PairingRequestInput{AppName: "x", Description: strings.Repeat("d", 501)}, "description"},
	} {
		_, err := e.svc.CreatePairing(e.ctx, "192.168.1.9", tc.in)
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields[tc.field] == "" {
			t.Errorf("%s: err = %v, want validation error on %s", tc.name, err, tc.field)
		}
	}
}

func TestPairingApprovalReturnsTokenExactlyOnce(t *testing.T) {
	e := newEnv(t)
	ops := e.channel("ops", protocol.ChannelPublic)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")

	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{
		Scopes: []string{"alerts:write", "messages:write"}, ChannelIDs: []string{ops.ID}, CreateChannel: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.App.Name != "netdiag" || res.App.Username != "netdiag" || len(res.ChannelIDs) != 2 || res.ChannelIDs[1] != ops.ID {
		t.Fatalf("approval = %+v", res)
	}
	// The bot is a member of "ops" and of the new public "netdiag-alerts".
	var alertsChannel string
	if err := e.db.Read().QueryRow(`SELECT id FROM channels WHERE name = 'netdiag-alerts' AND kind = 'public'`).Scan(&alertsChannel); err != nil || alertsChannel != res.ChannelIDs[0] {
		t.Fatalf("alerts channel = %q, %v", alertsChannel, err)
	}
	for _, id := range res.ChannelIDs {
		if n := e.count(`SELECT COUNT(*) FROM memberships WHERE channel_id = ? AND user_id = ?`, id, res.App.UserID); n != 1 {
			t.Errorf("bot is not a member of %s", id)
		}
	}

	// A wrong secret learns nothing, not even that the request exists.
	for _, secret := range []string{"", "wrong", p.PollSecret + "x", strings.ToUpper(p.PollSecret)} {
		if _, err := e.svc.PollPairing(e.ctx, "192.168.1.9", p.ID, secret); !errors.Is(err, chat.ErrNotFound) {
			t.Errorf("poll with secret %q: %v, want not found", secret, err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests WHERE state = 'approved'`); n != 1 {
		t.Fatal("wrong secrets disturbed the request")
	}

	st := e.poll(p)
	if st.State != protocol.PairingClaimed || !strings.HasPrefix(st.Token, "bub_") {
		t.Fatalf("first poll = %+v", st)
	}
	// The token works and carries the approved scopes.
	pr, err := e.authenticate("Bearer " + st.Token)
	if err != nil || pr == nil {
		t.Fatalf("claimed token rejected: %v, %v", pr, err)
	}
	if !slices.Equal(pr.Token.Scopes, []string{"messages:write", "alerts:write"}) || pr.Token.ChannelIDs != nil || pr.User.ID != res.App.UserID {
		t.Errorf("grant = %+v", pr.Token)
	}
	// Exactly once: later polls report claimed without a token, and nothing is left to decrypt.
	for range 3 {
		if again := e.poll(p); again.State != protocol.PairingClaimed || again.Token != "" {
			t.Errorf("repeat poll = %+v", again)
		}
	}
	// Exactly one token was minted, by the claim; the repeat polls minted none.
	if n := e.count(`SELECT COUNT(*) FROM app_tokens WHERE app_id = ?`, res.App.ID); n != 1 {
		t.Errorf("tokens of the app after claim = %d, want 1", n)
	}
	for _, action := range []string{"pairing.approve", "app.create", "token.create"} {
		if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action); n != 1 {
			t.Errorf("%s audit rows = %d", action, n)
		}
	}
	// A request nobody can poll again cannot be approved again.
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{}); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("second approval: %v", err)
	}
}

func TestPairingApprovalStoresNoTokenAtRest(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// (a) An approved request holds no token material: no token row exists and
	// the (unused) ciphertext column is empty.
	if n := e.count(`SELECT COUNT(*) FROM app_tokens`); n != 0 {
		t.Errorf("token rows after approval = %d, want 0", n)
	}
	var ct []byte
	if err := e.db.Read().QueryRow(`SELECT token_ciphertext FROM pairing_requests WHERE id = ?`, p.ID).Scan(&ct); err != nil || ct != nil {
		t.Errorf("token_ciphertext after approval = %v, %v", ct, err)
	}
	// (c) A wrong secret is not found, and it claims nothing.
	if _, err := e.svc.PollPairing(e.ctx, "192.168.1.9", p.ID, "wrong"); !errors.Is(err, chat.ErrNotFound) {
		t.Fatalf("wrong secret: %v, want not found", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM app_tokens`); n != 0 {
		t.Fatalf("wrong secret minted %d tokens", n)
	}
	// (b) The first poll returns the token, minted by the claim with the approved scopes.
	st := e.poll(p)
	if st.State != protocol.PairingClaimed || !strings.HasPrefix(st.Token, "bub_") {
		t.Fatalf("first poll = %+v", st)
	}
	pr, _ := e.authenticate("Bearer " + st.Token)
	if pr == nil || !slices.Equal(pr.Token.Scopes, []string{"alerts:write"}) || pr.User.ID != res.App.UserID {
		t.Errorf("claimed token = %+v", pr)
	}
	// The second poll gets the state but no token.
	if again := e.poll(p); again.State != protocol.PairingClaimed || again.Token != "" {
		t.Errorf("second poll = %+v", again)
	}
}

func TestPairingClaimSkipsAnAppDisabledAfterApproval(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	off, on := true, false
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, res.App.ID, UpdateAppInput{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if st := e.poll(p); st.State != protocol.PairingApproved || st.Token != "" {
		t.Errorf("poll of a disabled app = %+v", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM app_tokens`); n != 0 {
		t.Errorf("disabled app got %d tokens", n)
	}
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, res.App.ID, UpdateAppInput{Disabled: &on}); err != nil {
		t.Fatal(err)
	}
	if st := e.poll(p); st.State != protocol.PairingClaimed || !strings.HasPrefix(st.Token, "bub_") {
		t.Errorf("poll after re-enabling = %+v", st)
	}
}

func TestPairingExpiredRequestIsReportedExpired(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	e.clock.Advance(PairingTTL - time.Second)
	if st := e.poll(p); st.State != protocol.PairingPending {
		t.Errorf("just before expiry: %+v", st)
	}
	e.clock.Advance(time.Second)
	if st := e.poll(p); st.State != protocol.PairingExpired || st.Token != "" {
		t.Errorf("at expiry: %+v", st)
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{}); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("approving an expired request: %v", err)
	}
	if pending, _ := e.svc.ListPending(e.ctx, ""); len(pending) != 0 {
		t.Errorf("expired request listed: %+v", pending)
	}
	// The code of an expired request is free again.
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests WHERE state = 'pending'`); n != 0 {
		t.Errorf("pending rows = %d", n)
	}
}

func TestPairingApprovedButUnclaimedExpiresAndDisablesItsApp(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	e.clock.Advance(PairingTTL - time.Second)
	// An approval at the last second still leaves the app the claim window.
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Nobody polls within the window, which ends ClaimWindow after the approval.
	e.clock.Advance(ClaimWindow + 2*time.Second)
	if st := e.poll(p); st.State != protocol.PairingExpired || st.Token != "" {
		t.Fatalf("unclaimed approval after the window: %+v", st)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps WHERE id = ? AND disabled_at IS NOT NULL`, res.App.ID); n != 1 {
		t.Error("app of an unclaimed approval is still enabled")
	}
	if n := e.count(`SELECT COUNT(*) FROM app_tokens`); n != 0 {
		t.Errorf("unclaimed approval minted %d tokens", n)
	}
}

func TestPairingDenyAndStateGuards(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag")
	if err := e.svc.DenyPairing(e.ctx, e.aud, p.ID); err != nil {
		t.Fatal(err)
	}
	if st := e.poll(p); st.State != protocol.PairingDenied || st.Token != "" {
		t.Errorf("denied poll = %+v", st)
	}
	if err := e.svc.DenyPairing(e.ctx, e.aud, p.ID); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("second deny: %v", err)
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{Scopes: []string{"mcp"}}); !errors.Is(err, chat.ErrConflict) {
		t.Errorf("approving a denied request: %v", err)
	}
	if err := e.svc.DenyPairing(e.ctx, e.aud, "nope"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("unknown request: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'pairing.deny'`); n != 1 {
		t.Errorf("pairing.deny audit rows = %d", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM apps`); n != 0 {
		t.Errorf("denied request created %d apps", n)
	}

	// Approval without any scope (none requested, none chosen) is refused and creates nothing.
	q := e.pair("192.168.1.9", "noscope")
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, q.ID, protocol.ApprovePairingRequest{}); err == nil {
		t.Error("approval without scopes accepted")
	}
	if _, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, q.ID, protocol.ApprovePairingRequest{Scopes: []string{"mcp"}, ChannelIDs: []string{"nope"}}); err == nil {
		t.Error("approval with an unknown channel accepted")
	}
	if n := e.count(`SELECT COUNT(*) FROM apps`); n != 0 {
		t.Errorf("failed approvals left %d apps behind", n)
	}
	if st := e.poll(q); st.State != protocol.PairingPending {
		t.Errorf("failed approval changed the state: %+v", st)
	}
}

func TestPairingLimits(t *testing.T) {
	e := newEnv(t)
	// 5 requests per minute per source address.
	for i := range 5 {
		e.pair("192.168.1.9", fmt.Sprintf("app%d", i))
	}
	_, err := e.svc.CreatePairing(e.ctx, "192.168.1.9", protocol.PairingRequestInput{AppName: "sixth"})
	var rl *auth.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("sixth request from one IP: %v", err)
	}
	// Another address is not affected, up to 20 pending in total.
	for i := 5; i < MaxPendingPairings; i++ {
		e.pair(fmt.Sprintf("192.168.2.%d", i), fmt.Sprintf("app%d", i))
	}
	_, err = e.svc.CreatePairing(e.ctx, "192.168.3.1", protocol.PairingRequestInput{AppName: "twenty-first"})
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("21st pending request: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests`); n != MaxPendingPairings {
		t.Errorf("rows = %d, want %d", n, MaxPendingPairings)
	}
	// Once the old ones expire there is room again.
	e.clock.Advance(PairingTTL)
	if _, err := e.svc.CreatePairing(e.ctx, "192.168.3.1", protocol.PairingRequestInput{AppName: "after expiry"}); err != nil {
		t.Errorf("after expiry: %v", err)
	}
}

func TestPairingCodesAreUniqueAmongPending(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for i := range MaxPendingPairings {
		p := e.pair(fmt.Sprintf("10.0.%d.1", i), "app")
		if seen[p.Code] {
			t.Fatalf("duplicate pending code %s", p.Code)
		}
		seen[p.Code] = true
	}
}

func TestPairingOldRequestsArePurged(t *testing.T) {
	e := newEnv(t)
	e.pair("192.168.1.9", "old")
	e.clock.Advance(PairingTTL + pairingRetention + time.Minute)
	e.pair("192.168.1.10", "new")
	if n := e.count(`SELECT COUNT(*) FROM pairing_requests`); n != 1 {
		t.Errorf("rows after purge = %d, want 1", n)
	}
}

func TestPairingPollIsRateLimitedPerIP(t *testing.T) {
	e := newEnv(t)
	p := e.pair("192.168.1.9", "netdiag")
	var rl *auth.RateLimitedError
	limited := false
	for range PairingPollBurst + 5 {
		if _, err := e.svc.PollPairing(e.ctx, "192.168.1.77", p.ID, p.PollSecret); errors.As(err, &rl) {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("polling is not limited")
	}
	if _, err := e.svc.PollPairing(e.ctx, "192.168.1.78", p.ID, p.PollSecret); err != nil {
		t.Errorf("another address: %v", err)
	}
}

func TestPairingApprovalIsAtomic(t *testing.T) {
	e := newEnv(t)
	e.channel("taken-alerts", protocol.ChannelPrivate)
	p := e.pair("192.168.1.9", "taken", "alerts:write")
	// A private "taken-alerts" exists, so the alerts channel fails the whole approval.
	_, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{CreateChannel: true})
	if !errors.Is(err, chat.ErrConflict) {
		t.Fatalf("approval = %v, want conflict", err)
	}
	for _, q := range []string{`SELECT COUNT(*) FROM apps`, `SELECT COUNT(*) FROM app_tokens`, `SELECT COUNT(*) FROM users WHERE kind = 'app'`} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d after a failed approval", q, n)
		}
	}
	if st := e.poll(p); st.State != protocol.PairingPending {
		t.Errorf("state after failed approval: %+v", st)
	}
}

func TestPairingClaimListsTheBotChannels(t *testing.T) {
	channelNames := func(chs []protocol.PairingChannel) []string {
		var out []string
		for _, ch := range chs {
			out = append(out, ch.Name)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		create bool
		pick   []string
		want   []string
	}{
		{"created alerts channel", true, nil, []string{"netdiag-alerts"}},
		{"chosen existing channel", false, []string{"ops"}, []string{"ops"}},
		{"both, sorted by name", true, []string{"ops", "lab"}, []string{"lab", "netdiag-alerts", "ops"}},
		{"no channel", false, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			ids := map[string]string{}
			for _, name := range []string{"ops", "lab"} {
				ids[name] = e.channel(name, protocol.ChannelPublic).ID
			}
			var pick []string
			for _, name := range tc.pick {
				pick = append(pick, ids[name])
			}
			p := e.pair("192.168.1.9", "netdiag", "alerts:write")
			res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ChannelIDs: pick, CreateChannel: tc.create})
			if err != nil {
				t.Fatal(err)
			}
			if tc.create {
				ids["netdiag-alerts"] = res.ChannelIDs[0]
			}
			st := e.poll(p)
			if st.State != protocol.PairingClaimed || !strings.HasPrefix(st.Token, "bub_") {
				t.Fatalf("claim = %+v", st)
			}
			if got := channelNames(st.Channels); !slices.Equal(got, tc.want) {
				t.Errorf("claim channels = %v, want %v", got, tc.want)
			}
			for _, ch := range st.Channels {
				if ch.ID != ids[ch.Name] {
					t.Errorf("channel %s has id %s, want %s", ch.Name, ch.ID, ids[ch.Name])
				}
			}
			if tc.want == nil && st.Channels != nil {
				t.Errorf("claim channels = %#v, want nil", st.Channels)
			}
			// The answer after the claim has neither the token nor the channels.
			if again := e.poll(p); again.State != protocol.PairingClaimed || again.Token != "" || again.Channels != nil {
				t.Errorf("poll after the claim = %+v", again)
			}
		})
	}
}

func TestPairingClaimChannelsAreReadAtTheClaim(t *testing.T) {
	e := newEnv(t)
	ops := e.channel("ops", protocol.ChannelPublic)
	old := e.channel("old", protocol.ChannelPublic)
	p := e.pair("192.168.1.9", "netdiag", "alerts:write")
	res, err := e.svc.ApprovePairing(e.ctx, e.admin, e.aud, p.ID, protocol.ApprovePairingRequest{ChannelIDs: []string{ops.ID, old.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// Between the approval and the claim: a private channel gains the bot,
	// "old" is archived, and the bot is in a direct message (which has no
	// name and is not listed).
	secret := e.channel("secret", protocol.ChannelPrivate, res.App.UserID)
	if _, err := e.chat.ArchiveChannel(e.ctx, e.admin, old.ID); err != nil {
		t.Fatal(err)
	}
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		id := tx.NewID()
		if _, err := tx.Exec(`INSERT INTO channels (id, kind, dm_key, created_at) VALUES (?, 'dm', ?, ?)`,
			id, chat.DMKey([]string{e.admin.ID, res.App.UserID}), tx.Now()); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO memberships (channel_id, user_id, joined_at) VALUES (?, ?, ?), (?, ?, ?)`,
			id, e.admin.ID, tx.Now(), id, res.App.UserID, tx.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	st := e.poll(p)
	want := []protocol.PairingChannel{{ID: ops.ID, Name: "ops"}, {ID: secret.ID, Name: "secret"}}
	if st.State != protocol.PairingClaimed || !slices.Equal(st.Channels, want) {
		t.Errorf("claim = %+v, want channels %+v", st, want)
	}
}
