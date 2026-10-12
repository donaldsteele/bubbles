package apps

import (
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

func TestAppsCreateAppMakesBotAccount(t *testing.T) {
	e := newEnv(t)
	app := e.app("Net Diag")
	if app.Username != "net-diag" || app.Name != "Net Diag" || app.OwnerUserID == nil || *app.OwnerUserID != e.admin.ID {
		t.Fatalf("app = %+v", app)
	}
	var kind, role string
	var pw *string
	err := e.db.Read().QueryRow(`SELECT kind, role, pw_hash FROM users WHERE id = ?`, app.UserID).Scan(&kind, &role, &pw)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "app" || role != "member" || pw != nil {
		t.Errorf("bot = kind %q role %q pw_hash %v", kind, role, pw)
	}
	// A second app with the same name gets the next free username.
	if again := e.app("Net Diag"); again.Username != "net-diag-2" {
		t.Errorf("second username = %q", again.Username)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'app.create' AND target_id = ?`, app.ID); n != 1 {
		t.Errorf("app.create audit rows = %d", n)
	}
}

func TestAppsCreateAppValidation(t *testing.T) {
	e := newEnv(t)
	e.app("Taken")
	for _, tc := range []struct {
		name  string
		in    CreateAppInput
		field string
	}{
		{"empty name", CreateAppInput{Name: "  "}, "name"},
		{"long name", CreateAppInput{Name: strings.Repeat("x", 65)}, "name"},
		{"control char", CreateAppInput{Name: "bad\x01name"}, "name"},
		{"long description", CreateAppInput{Name: "ok", Description: strings.Repeat("d", 501)}, "description"},
		{"bad username", CreateAppInput{Name: "ok", Username: "Not Valid!"}, "username"},
		{"short username", CreateAppInput{Name: "ok", Username: "a"}, "username"},
		{"taken username", CreateAppInput{Name: "ok", Username: "TAKEN"}, "username"},
	} {
		_, err := e.svc.CreateApp(e.ctx, e.aud, tc.in)
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields[tc.field] == "" {
			t.Errorf("%s: err = %v, want validation error on %s", tc.name, err, tc.field)
		}
	}
	// An explicit username is used as given (lower-cased).
	app, err := e.svc.CreateApp(e.ctx, e.aud, CreateAppInput{Name: "Whatever", Username: "My.Bot"})
	if err != nil || app.Username != "my.bot" {
		t.Errorf("explicit username: %+v, %v", app, err)
	}
	// A name with nothing usable still yields a valid username.
	if app := e.app("!!!"); app.Username != "app" {
		t.Errorf("derived username for !!! = %q", app.Username)
	}
}

func TestAppsDeriveUsername(t *testing.T) {
	for name, want := range map[string]string{
		"netdiag":                     "netdiag",
		"Net Diag Sender":             "net-diag-sender",
		"  --Odd__Name.v2--  ":        "odd__name.v2",
		"Ünïcode bot":                 "n-code-bot",
		strings.Repeat("a", 40):       strings.Repeat("a", 32),
		"x":                           "app-x",
		"CI/CD pipeline (prod) #1":    "ci-cd-pipeline-prod-1",
		strings.Repeat("ab ", 20)[:5]: "ab-ab",
	} {
		got := deriveUsername(name)
		if got != want || !validUsername(got) {
			t.Errorf("deriveUsername(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAppsIssueTokenStoresOnlyHash(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	ch := e.channel("ops", protocol.ChannelPublic)
	exp := e.clock.Now().Add(time.Hour)
	tok, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, IssueTokenInput{
		Scopes: []string{"messages:write", "alerts:write", "alerts:write"}, ChannelIDs: []string{ch.ID, ch.ID}, ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Secret, "bub_") || len(tok.Secret) != 4+52 {
		t.Errorf("secret shape = %q (len %d)", tok.Secret, len(tok.Secret))
	}
	if tok.Info.Prefix != tok.Secret[:8] || len(tok.Info.Prefix) != 8 {
		t.Errorf("prefix = %q", tok.Info.Prefix)
	}
	if !slices.Equal(tok.Info.Scopes, []string{"messages:write", "alerts:write"}) || !slices.Equal(tok.Info.ChannelIDs, []string{ch.ID}) || tok.Info.ExpiresAt == nil {
		t.Errorf("info = %+v", tok.Info)
	}
	var hash []byte
	var scopes string
	if err := e.db.Read().QueryRow(`SELECT token_hash, scopes FROM app_tokens WHERE id = ?`, tok.Info.ID).Scan(&hash, &scopes); err != nil {
		t.Fatal(err)
	}
	if string(hash) != string(auth.HashToken(tok.Secret)) {
		t.Error("stored hash is not the SHA-256 of the secret")
	}
	// The secret appears nowhere in the database, not even in the audit log.
	for _, table := range []string{"app_tokens", "audit_log", "events", "users", "apps"} {
		rows, err := e.db.Read().Query(`SELECT * FROM ` + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				switch x := v.(type) {
				case string:
					if strings.Contains(x, tok.Secret) {
						t.Errorf("secret found in %s", table)
					}
				case []byte:
					if strings.Contains(string(x), tok.Secret) {
						t.Errorf("secret found in %s", table)
					}
				}
			}
		}
		rows.Close()
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'token.create' AND target_id = ?`, tok.Info.ID); n != 1 {
		t.Errorf("token.create audit rows = %d", n)
	}
}

func TestAppsIssueTokenValidation(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	past := e.clock.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name  string
		in    IssueTokenInput
		field string
	}{
		{"no scopes", IssueTokenInput{}, "scopes"},
		{"unknown scope", IssueTokenInput{Scopes: []string{"messages:read", "root"}}, "scopes"},
		{"unknown channel", IssueTokenInput{Scopes: []string{"messages:read"}, ChannelIDs: []string{"nope"}}, "channel_ids"},
		{"expired", IssueTokenInput{Scopes: []string{"messages:read"}, ExpiresAt: &past}, "expires_at"},
	} {
		_, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, tc.in)
		var ve *chat.ValidationError
		if !errors.As(err, &ve) || ve.Fields[tc.field] == "" {
			t.Errorf("%s: err = %v, want validation error on %s", tc.name, err, tc.field)
		}
	}
	if _, err := e.svc.IssueToken(e.ctx, e.aud, "nope", IssueTokenInput{Scopes: []string{"mcp"}}); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("unknown app: %v", err)
	}
	for _, s := range protocol.AppScopes {
		if _, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, IssueTokenInput{Scopes: []string{s}}); err != nil {
			t.Errorf("scope %s rejected: %v", s, err)
		}
	}
}

func TestAppsBearerAuthentication(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	ch := e.channel("ops", protocol.ChannelPublic)
	tok, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, IssueTokenInput{Scopes: []string{"messages:read"}, ChannelIDs: []string{ch.ID}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.authenticate("Bearer " + tok.Secret)
	if err != nil || p == nil {
		t.Fatalf("authenticate = %v, %v", p, err)
	}
	if p.Via != auth.ViaBearer || p.Session != nil || p.User.ID != app.UserID || p.User.Kind != "app" {
		t.Errorf("principal = %+v user %+v", p, p.User)
	}
	if p.Token.ID != tok.Info.ID || p.Token.AppID != app.ID || !slices.Equal(p.Token.Scopes, []string{"messages:read"}) || !slices.Equal(p.Token.ChannelIDs, []string{ch.ID}) {
		t.Errorf("grant = %+v", p.Token)
	}
	// The scheme is case-insensitive; anything else is not a token.
	if p, _ := e.authenticate("bearer " + tok.Secret); p == nil {
		t.Error("lower-case scheme rejected")
	}
	for _, h := range []string{
		"", "Bearer", "Bearer ", "Bearer bub_", "Bearer " + tok.Secret + "x", "Bearer " + strings.ToUpper(tok.Secret),
		"Bearer " + tok.Secret[4:], "Basic " + tok.Secret, "Bearer bub_" + strings.Repeat("a", 200), tok.Secret,
	} {
		if p, err := e.authenticate(h); p != nil || err != nil {
			t.Errorf("header %q: principal %v err %v, want (nil, nil)", h, p, err)
		}
	}
	// A token without an allowlist carries nil, not an empty list.
	open := e.token(app.ID, "messages:read")
	if p, _ := e.authenticate("Bearer " + open.Secret); p == nil || p.Token.ChannelIDs != nil {
		t.Errorf("unrestricted token grant = %+v", p)
	}
}

func TestAppsRevokedExpiredAndDisabledTokensAreRejected(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	live := func(secret string) bool {
		p, err := e.authenticate("Bearer " + secret)
		if err != nil {
			t.Fatal(err)
		}
		return p != nil
	}

	revoked := e.token(app.ID, "messages:read")
	if !live(revoked.Secret) {
		t.Fatal("fresh token rejected")
	}
	if err := e.svc.RevokeToken(e.ctx, e.aud, app.ID, revoked.Info.ID); err != nil {
		t.Fatal(err)
	}
	if live(revoked.Secret) {
		t.Error("revoked token still accepted on the next request")
	}
	if err := e.svc.RevokeToken(e.ctx, e.aud, app.ID, revoked.Info.ID); err != nil {
		t.Errorf("second revoke: %v", err)
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'token.revoke'`); n != 1 {
		t.Errorf("token.revoke audit rows = %d, want 1", n)
	}
	other := e.app("other")
	if err := e.svc.RevokeToken(e.ctx, e.aud, other.ID, revoked.Info.ID); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("revoke through another app: %v", err)
	}

	exp := e.clock.Now().Add(time.Hour)
	expiring, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, IssueTokenInput{Scopes: []string{"messages:read"}, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	if !live(expiring.Secret) {
		t.Fatal("unexpired token rejected")
	}
	e.clock.Advance(time.Hour)
	if live(expiring.Secret) {
		t.Error("expired token accepted")
	}

	tok := e.token(app.ID, "messages:read")
	yes := true
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if live(tok.Secret) {
		t.Error("token of a disabled app accepted")
	}
	no := false
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Disabled: &no}); err != nil {
		t.Fatal(err)
	}
	if !live(tok.Secret) {
		t.Error("token rejected after the app was enabled again")
	}
	if n := e.count(`SELECT COUNT(*) FROM audit_log WHERE action IN ('app.disable', 'app.enable')`); n != 2 {
		t.Errorf("disable/enable audit rows = %d", n)
	}

	// A deactivated bot account is locked out as well.
	err = e.db.Do(e.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE users SET deactivated_at = ? WHERE id = ?`, tx.Now(), app.UserID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if live(tok.Secret) {
		t.Error("token of a deactivated bot accepted")
	}
}

func TestAppsLastUsedIsThrottledToOncePerMinute(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	tok := e.token(app.ID, "messages:read")
	lastUsed := func() (int64, bool) {
		var ms *int64
		if err := e.db.Read().QueryRow(`SELECT last_used_at FROM app_tokens WHERE id = ?`, tok.Info.ID).Scan(&ms); err != nil {
			t.Fatal(err)
		}
		if ms == nil {
			return 0, false
		}
		return *ms, true
	}
	if _, ok := lastUsed(); ok {
		t.Fatal("last_used_at set before first use")
	}
	use := func() {
		t.Helper()
		if p, err := e.authenticate("Bearer " + tok.Secret); p == nil || err != nil {
			t.Fatalf("authenticate: %v, %v", p, err)
		}
	}
	use()
	first, ok := lastUsed()
	if !ok || first != e.clock.Now().UnixMilli() {
		t.Fatalf("first use: last_used_at = %d (%v), want %d", first, ok, e.clock.Now().UnixMilli())
	}
	for range 5 {
		e.clock.Advance(10 * time.Second)
		use()
	}
	if got, _ := lastUsed(); got != first {
		t.Errorf("last_used_at moved to %d within a minute of %d", got, first)
	}
	e.clock.Advance(20 * time.Second) // 70 s after the first use
	use()
	if got, _ := lastUsed(); got != e.clock.Now().UnixMilli() {
		t.Errorf("last_used_at = %d after a minute, want %d", got, e.clock.Now().UnixMilli())
	}
}

func TestAppsTokenLimiterIs10PerSecondBurst30(t *testing.T) {
	e := newEnv(t)
	l := e.svc.Limiter()
	for i := range 30 {
		if ok, _ := l.Allow("tok"); !ok {
			t.Fatalf("request %d of the burst refused", i+1)
		}
	}
	ok, wait := l.Allow("tok")
	if ok || wait <= 0 || wait > 200*time.Millisecond {
		t.Errorf("31st request: ok=%v wait=%v, want refused within 100ms", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Error("another token shares the bucket")
	}
	e.clock.Advance(time.Second)
	for i := range 10 {
		if ok, _ := l.Allow("tok"); !ok {
			t.Fatalf("request %d after a second refused", i+1)
		}
	}
	if ok, _ := l.Allow("tok"); ok {
		t.Error("more than 10 tokens refilled in a second")
	}
}

func TestAppsUpdateAppRenamesBot(t *testing.T) {
	e := newEnv(t)
	app := e.app("old")
	name, desc := "New Name", "does things"
	got, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Name: &name, Description: &desc})
	if err != nil || got.Name != name || got.Description != desc || got.Username != "old" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	var display string
	if err := e.db.Read().QueryRow(`SELECT display_name FROM users WHERE id = ?`, app.UserID).Scan(&display); err != nil || display != name {
		t.Errorf("bot display name = %q, %v", display, err)
	}
	empty := " "
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Name: &empty}); err == nil {
		t.Error("blank name accepted")
	}
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, "nope", UpdateAppInput{Name: &name}); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("unknown app: %v", err)
	}
	list, err := e.svc.ListApps(e.ctx)
	if err != nil || len(list) != 1 || list[0].Tokens == nil {
		t.Errorf("list = %+v, %v", list, err)
	}
}

func TestAppsListShowsTokensWithoutSecrets(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	a := e.token(app.ID, "messages:read")
	b := e.token(app.ID, "alerts:write")
	if err := e.svc.RevokeToken(e.ctx, e.aud, app.ID, a.Info.ID); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.ListApps(e.ctx)
	if err != nil || len(list) != 1 || len(list[0].Tokens) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	toks := list[0].Tokens
	if toks[0].ID != a.Info.ID || toks[0].RevokedAt == nil || toks[1].ID != b.Info.ID || toks[1].RevokedAt != nil {
		t.Errorf("tokens = %+v", toks)
	}
	self, err := e.svc.Self(e.ctx, app.ID, b.Info.ID)
	if err != nil || self.App.ID != app.ID || self.Token.ID != b.Info.ID || len(self.App.Tokens) != 0 {
		t.Errorf("self = %+v, %v", self, err)
	}
	if _, err := e.svc.Self(e.ctx, app.ID, "nope"); !errors.Is(err, chat.ErrNotFound) {
		t.Errorf("self with a foreign token: %v", err)
	}
}

func TestAppsConnectionString(t *testing.T) {
	fp := strings.Repeat("ab12", 16)
	spaced := strings.ToUpper(strings.Join([]string{fp[0:4], fp[4:8], fp[8:12], fp[12:16], fp[16:20], fp[20:24], fp[24:28], fp[28:32], fp[32:36], fp[36:40], fp[40:44], fp[44:48], fp[48:52], fp[52:56], fp[56:60], fp[60:64]}, " "))
	for _, tc := range []struct {
		name string
		e    Endpoint
		want string
	}{
		{"ipv4 spaced upper-case fingerprint", Endpoint{Host: "192.168.1.5", Port: 8443, Fingerprint: spaced}, "bubbles://192.168.1.5:8443/?token=bub_x&fp=sha256:" + fp},
		{"ipv6", Endpoint{Host: "fd00::5", Port: 8443, Fingerprint: "sha256:" + fp}, "bubbles://[fd00::5]:8443/?token=bub_x&fp=sha256:" + fp},
		{"colon notation", Endpoint{Host: "bubbles.local", Port: 443, Fingerprint: "AB:12:AB:12"}, "bubbles://bubbles.local:443/?token=bub_x&fp=sha256:ab12ab12"},
		{"no fingerprint", Endpoint{Host: "10.0.0.1", Port: 8443}, "bubbles://10.0.0.1:8443/?token=bub_x"},
	} {
		if got := tc.e.ConnectionString("bub_x"); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestAppsNormalizeScopes(t *testing.T) {
	got, err := NormalizeScopes("scopes", []string{"mcp", "messages:read", "mcp", "alerts:write"})
	if err != nil || !slices.Equal(got, []string{"messages:read", "alerts:write", "mcp"}) {
		t.Errorf("normalize = %v, %v", got, err)
	}
	for _, bad := range []string{"", "admin", "messages:*", "MESSAGES:READ"} {
		if _, err := NormalizeScopes("scopes", []string{bad}); err == nil {
			t.Errorf("scope %q accepted", bad)
		}
	}
	if !HasScope(got, ScopeMCP) || HasScope(got, ScopeDMWrite) {
		t.Error("HasScope")
	}
}
