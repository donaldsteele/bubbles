package apps

import (
	"testing"
	"time"
)

func TestRevokeAndDisableAppendBearerCloseEvents(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	tok := e.token(app.ID, "messages:read")

	if err := e.svc.RevokeToken(e.ctx, e.aud, app.ID, tok.Info.ID); err != nil {
		t.Fatal(err)
	}
	byToken := `%"token_id":"` + tok.Info.ID + `"%`
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ? AND payload LIKE ?`, app.UserID, byToken); n != 1 {
		t.Fatalf("session.revoked events for the revoked token = %d, want 1", n)
	}
	// Revoking again changes nothing, so it appends nothing.
	if err := e.svc.RevokeToken(e.ctx, e.aud, app.ID, tok.Info.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ?`, app.UserID); n != 1 {
		t.Fatalf("session.revoked events after a repeat revoke = %d, want 1", n)
	}

	disabled := true
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ? AND payload LIKE '%"app_disabled":true%'`, app.UserID); n != 1 {
		t.Fatalf("app_disabled events = %d, want 1", n)
	}
	enabled := false
	if _, err := e.svc.UpdateApp(e.ctx, e.aud, app.ID, UpdateAppInput{Disabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM events WHERE type = 'session.revoked' AND user_id = ?`, app.UserID); n != 2 {
		// Enabling is not a close; the only other event is the disable's.
		t.Fatalf("session.revoked events after enable = %d, want 2 (revoke and disable)", n)
	}
}

func TestBearerGrantCarriesTokenExpiry(t *testing.T) {
	e := newEnv(t)
	app := e.app("bot")
	exp := e.clock.Now().Add(time.Hour)
	tok, err := e.svc.IssueToken(e.ctx, e.aud, app.ID, IssueTokenInput{Scopes: []string{"messages:read"}, ExpiresAt: &exp})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.authenticate("Bearer " + tok.Secret)
	if err != nil || p == nil {
		t.Fatalf("authenticate: %v, %v", p, err)
	}
	if p.Token.ExpiresAt != exp.UnixMilli() || p.Token.ID != tok.Info.ID {
		t.Fatalf("grant = %+v, want expiry %d and id %s", p.Token, exp.UnixMilli(), tok.Info.ID)
	}
	forever := e.token(app.ID, "messages:read")
	if p, err := e.authenticate("Bearer " + forever.Secret); err != nil || p.Token.ExpiresAt != 0 {
		t.Fatalf("non-expiring grant expiry = %d (%v)", p.Token.ExpiresAt, err)
	}
}
