package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/tlsca"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// fakeAdminHost stands in for the composition root.
type fakeAdminHost struct {
	start time.Time
	byo   bool
}

func (f fakeAdminHost) Host() HostStatus {
	mode := tlsca.ModeLocalCA
	if f.byo {
		mode = tlsca.ModeBYO
	}
	return HostStatus{
		StartedAt: f.start, Connections: 3,
		HTTPSURLs: []string{"https://192.168.1.20:28225/"}, OnboardingURLs: []string{"http://192.168.1.20:28226/"},
		TLS: tlsca.Status{
			Mode: mode, CAFingerprint: "AB:CD", LeafFingerprint: "EF:01",
			LeafNotAfter: f.start.Add(90 * 24 * time.Hour), SANs: []string{"192.168.1.20", "bubbles.local"},
		},
	}
}

func (f fakeAdminHost) ReissueTLS() error {
	if f.byo {
		return tlsca.ErrBYO
	}
	return nil
}

const adminAPIPath = "/api/v1/admin"

func TestAdminEveryEndpointIsAdminOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	member := e.newMember("alice", "192.168.1.20")
	anon := e.client("")
	app := e.newBot(admin, "bot", []string{"messages:read"}, nil)

	doc, _ := loadSpec(t)
	n := 0
	for path, item := range doc.Paths.Map() {
		if !strings.HasPrefix(path, "/admin/") {
			continue
		}
		concrete := strings.NewReplacer("{id}", "x", "{token_id}", "x").Replace(path)
		for method := range item.Operations() {
			n++
			var body any
			if method != http.MethodGet && method != http.MethodDelete {
				body = map[string]any{}
			}
			full := "/api/v1" + concrete
			wantStatus(t, anon.do(method, full, body), http.StatusUnauthorized, CodeUnauthorized)
			wantStatus(t, member.do(method, full, body), http.StatusForbidden, CodeForbidden)
			wantStatus(t, app.do(e, method, full, body), http.StatusForbidden, CodeForbidden)
			if method != http.MethodGet {
				// A valid admin session still needs a matching Origin.
				wantStatus(t, admin.do(method, full, body, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }),
					http.StatusForbidden, CodeForbidden)
			}
		}
	}
	if n < 25 {
		t.Fatalf("only %d admin operations found in the spec", n)
	}
}

func TestAdminOverview(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	e.clock.Advance(90 * time.Second)

	rec := admin.do(http.MethodGet, adminAPIPath+"/overview", nil)
	wantStatus(t, rec, http.StatusOK, "")
	o := decodeBody[protocol.AdminOverview](t, rec)
	if o.InstanceName != "Home" || o.Version != "v9.9.9" || o.Users != 1 || o.Connections != 3 || o.UptimeSeconds != 90 {
		t.Errorf("overview = %+v", o)
	}
	if o.CAFingerprint != "AB:CD" || o.TLSMode != "local-ca" || o.CertNotAfter == "" ||
		len(o.HTTPSURLs) != 1 || len(o.OnboardingURLs) != 1 || o.DiskFreeBytes == nil || *o.DiskFreeBytes <= 0 {
		t.Errorf("overview host data = %+v", o)
	}
	// The admin set the server up from a LAN address, so a device did connect.
	if o.ClientIsolationHint {
		t.Error("isolation hint although a LAN client signed in")
	}
}

func TestAdminOverviewIsolationHintWhenOnlyLoopbackConnected(t *testing.T) {
	e := newEnv(t)
	token, err := e.svc.InitSetup(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	local := e.client("127.0.0.1")
	wantStatus(t, local.do(http.MethodPost, "/api/v1/setup", protocol.SetupRequest{
		SetupToken: token, Username: "admin", Password: goodPassword, DisplayName: "Admin", InstanceName: "Home",
	}), http.StatusCreated, "")
	rec := local.do(http.MethodGet, adminAPIPath+"/overview", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if !decodeBody[protocol.AdminOverview](t, rec).ClientIsolationHint {
		t.Error("no isolation hint although only loopback signed in")
	}
}

func TestAdminSettingsGetPatchAndAudit(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()

	rec := admin.do(http.MethodGet, adminAPIPath+"/settings", nil)
	wantStatus(t, rec, http.StatusOK, "")
	s := decodeBody[protocol.AdminSettings](t, rec)
	if s.InstanceName != "Home" || s.UploadMaxBytes != 100<<20 || s.PushPayload != "minimal" || !s.WebResetEnabled || s.AllowedCIDRs == nil {
		t.Errorf("defaults = %+v", s)
	}

	rec = admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"open_registration":true,"default_retention_days":30,"allowed_cidrs":["100.64.0.0/10"],"push_payload":"full","web_push_enabled":true,"upload_max_bytes":1048576}`)
	wantStatus(t, rec, http.StatusOK, "")
	s = decodeBody[protocol.AdminSettings](t, rec)
	if !s.OpenRegistration || s.DefaultRetentionDays == nil || *s.DefaultRetentionDays != 30 || s.PushPayload != "full" ||
		!s.WebPushEnabled || s.UploadMaxBytes != 1<<20 || len(s.AllowedCIDRs) != 1 {
		t.Errorf("patched = %+v", s)
	}
	// null clears the default retention.
	rec = admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"default_retention_days":null}`)
	wantStatus(t, rec, http.StatusOK, "")
	if decodeBody[protocol.AdminSettings](t, rec).DefaultRetentionDays != nil {
		t.Error("retention not cleared")
	}

	// One bad value rejects the whole request and names every bad key.
	rec = admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"session_idle_days":500,"mdns_enabled":false,"nope":1,"allowed_cidrs":["x"],"alert_renotify_s":1,"push_payload":"loud","instance_name":" ","upload_max_bytes":0}`)
	er := wantStatus(t, rec, http.StatusBadRequest, CodeBadRequest)
	for _, k := range []string{"session_idle_days", "nope", "allowed_cidrs", "alert_renotify_s", "push_payload", "instance_name", "upload_max_bytes"} {
		if er.FieldErrors()[k] == "" {
			t.Errorf("no reason for %s: %v", k, er.FieldErrors())
		}
	}
	if !e.st.Values().MDNSEnabled {
		t.Error("a rejected PATCH changed mdns_enabled")
	}
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/settings", `{}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/settings", `{"open_registration":"yes"}`), http.StatusBadRequest, CodeBadRequest)

	// Every changed key is audited; the audit endpoint filters and pages.
	rec = admin.do(http.MethodGet, adminAPIPath+"/audit?action=settings.update&limit=3", nil)
	wantStatus(t, rec, http.StatusOK, "")
	p1 := decodeBody[protocol.AuditPage](t, rec)
	if len(p1.Items) != 3 || p1.NextCursor == nil {
		t.Fatalf("page 1 = %d items, cursor %v", len(p1.Items), p1.NextCursor)
	}
	rec = admin.do(http.MethodGet, adminAPIPath+"/audit?action=settings.update&limit=50&cursor="+url.QueryEscape(*p1.NextCursor), nil)
	wantStatus(t, rec, http.StatusOK, "")
	p2 := decodeBody[protocol.AuditPage](t, rec)
	if len(p2.Items) != 5 || p2.NextCursor != nil {
		t.Fatalf("page 2 = %d items, cursor %v", len(p2.Items), p2.NextCursor)
	}
	seen := map[string]bool{}
	for _, it := range append(p1.Items, p2.Items...) {
		if it.Action != "settings.update" || it.ActorKind != "user" || seen[it.ID] {
			t.Errorf("entry = %+v", it)
		}
		seen[it.ID] = true
	}
	wantStatus(t, admin.do(http.MethodGet, adminAPIPath+"/audit?cursor=***", nil), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodGet, adminAPIPath+"/audit?limit=0", nil), http.StatusBadRequest, CodeBadRequest)
}

func TestAdminUsers(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	e.newBot(admin, "bot", []string{"messages:read"}, nil)

	rec := admin.do(http.MethodGet, adminAPIPath+"/users", nil)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeBody[protocol.AdminUserList](t, rec)
	if len(list.Items) != 2 {
		t.Fatalf("users = %+v (bots belong under /admin/apps)", list.Items)
	}

	rec = admin.do(http.MethodPatch, adminAPIPath+"/users/"+alice.id, protocol.UpdateAdminUserRequest{Role: ptrTo("guest")})
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.User](t, rec); got.Role != "guest" || got.Deactivated {
		t.Errorf("patched = %+v", got)
	}
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/"+alice.id, `{"role":"root"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/"+alice.id, `{}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/nope", `{"role":"member"}`), http.StatusNotFound, CodeNotFound)
	// The caller cannot lock themselves out.
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/"+admin.id, `{"deactivated":true}`), http.StatusConflict, CodeConflict)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/"+admin.id, `{"role":"member"}`), http.StatusConflict, CodeConflict)

	// Deactivation ends the session at once.
	wantStatus(t, alice.do(http.MethodGet, "/api/v1/me", nil), http.StatusOK, "")
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/users/"+alice.id, `{"deactivated":true}`), http.StatusOK, "")
	wantStatus(t, alice.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)

	// Revoke sessions and the one-time reset link.
	bob := e.newMember("bob", "192.168.1.21")
	wantStatus(t, admin.do(http.MethodDelete, adminAPIPath+"/users/"+bob.id+"/sessions", nil), http.StatusNoContent, "")
	wantStatus(t, bob.do(http.MethodGet, "/api/v1/me", nil), http.StatusUnauthorized, CodeUnauthorized)
	wantStatus(t, admin.do(http.MethodDelete, adminAPIPath+"/users/nope/sessions", nil), http.StatusNotFound, CodeNotFound)

	rec = admin.do(http.MethodPost, adminAPIPath+"/users/"+bob.id+"/password-reset", nil)
	wantStatus(t, rec, http.StatusCreated, "")
	link := decodeBody[protocol.PasswordResetLink](t, rec)
	if !strings.HasPrefix(link.URL, "https://") || !strings.Contains(link.URL, "/reset/") || link.ExpiresAt == "" {
		t.Fatalf("link = %+v", link)
	}
	code := link.URL[strings.LastIndex(link.URL, "/")+1:]
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/password-resets/"+code, protocol.ResetPasswordRequest{Password: "another long passphrase"}), http.StatusNoContent, "")
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/users/"+alice.id+"/password-reset", nil), http.StatusNotFound, CodeNotFound)
}

func ptrTo[T any](v T) *T { return &v }

func TestAdminInvitesAndSecretsAppearOnce(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()

	rec := admin.do(http.MethodPost, adminAPIPath+"/invites", protocol.CreateInviteRequest{Role: "guest", Note: "event", ExpiresInSeconds: 3600, MaxUses: 5})
	wantStatus(t, rec, http.StatusCreated, "")
	inv := decodeBody[protocol.CreatedInvite](t, rec)
	if inv.Role != "guest" || inv.Note != "event" || inv.MaxUses != 5 || !strings.Contains(inv.URL, "/invite/") {
		t.Fatalf("invite = %+v", inv)
	}
	inviteCode := inv.URL[strings.LastIndex(inv.URL, "/")+1:]

	rec = admin.do(http.MethodPost, adminAPIPath+"/users/"+admin.id+"/password-reset", nil)
	wantStatus(t, rec, http.StatusCreated, "")
	resetLink := decodeBody[protocol.PasswordResetLink](t, rec)
	resetCode := resetLink.URL[strings.LastIndex(resetLink.URL, "/")+1:]

	// The codes are in no other admin answer.
	for _, p := range []string{"/invites", "/users", "/audit?limit=100", "/overview", "/settings", "/channels", "/apps", "/tls"} {
		body := admin.do(http.MethodGet, adminAPIPath+p, nil).Body.String()
		if strings.Contains(body, inviteCode) || strings.Contains(body, resetCode) {
			t.Errorf("GET %s leaks a one-time code", p)
		}
	}
	rec = admin.do(http.MethodGet, adminAPIPath+"/invites", nil)
	list := decodeBody[protocol.AdminInviteList](t, rec)
	if len(list.Items) != 1 || list.Items[0].ID != inv.ID || list.Items[0].UseCount != 0 {
		t.Fatalf("list = %+v", list)
	}
	// The link works, then the invite is revoked.
	c := e.client("192.168.1.30")
	wantStatus(t, c.do(http.MethodPost, "/api/v1/invites/"+inviteCode+"/accept", protocol.AcceptInviteRequest{Username: "guesty", Password: goodPassword}), http.StatusCreated, "")
	wantStatus(t, admin.do(http.MethodDelete, adminAPIPath+"/invites/"+inv.ID, nil), http.StatusNoContent, "")
	wantStatus(t, admin.do(http.MethodDelete, adminAPIPath+"/invites/"+inv.ID, nil), http.StatusNoContent, "")
	wantStatus(t, admin.do(http.MethodDelete, adminAPIPath+"/invites/nope", nil), http.StatusNotFound, CodeNotFound)
	wantStatus(t, e.client("").do(http.MethodPost, "/api/v1/invites/"+inviteCode+"/accept", protocol.AcceptInviteRequest{Username: "late", Password: goodPassword}), http.StatusNotFound, "")

	// Defaults and validation.
	rec = admin.do(http.MethodPost, adminAPIPath+"/invites", nil)
	wantStatus(t, rec, http.StatusCreated, "")
	if d := decodeBody[protocol.CreatedInvite](t, rec); d.Role != "member" || d.MaxUses != 1 {
		t.Errorf("defaults = %+v", d)
	}
	for _, body := range []string{`{"role":"root"}`, `{"max_uses":5000}`, `{"expires_in_seconds":5}`, `{"expires_in_seconds":99999999}`, `{"expires_in_seconds":-1}`} {
		wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/invites", body), http.StatusBadRequest, CodeBadRequest)
	}
}

func TestAdminChannelsNeverExposeContent(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	alice := e.newMember("alice", "192.168.1.20")
	bob := e.newMember("bob", "192.168.1.21")
	secret := alice.createChannel(t, "secret-plans", protocol.ChannelPrivate, bob)
	pub := alice.createChannel(t, "town-square", protocol.ChannelPublic)
	const body = "the-launch-code-is-1234"
	alice.post(t, secret.ID, body)
	dm := alice.do(http.MethodPost, "/api/v1/dms", protocol.StartDMRequest{UserIDs: []string{bob.id}})
	wantStatus(t, dm, http.StatusCreated, "")
	dmCh := decodeBody[protocol.Channel](t, dm)
	alice.post(t, dmCh.ID, body+"-dm")

	rec := admin.do(http.MethodGet, adminAPIPath+"/channels", nil)
	wantStatus(t, rec, http.StatusOK, "")
	list := decodeBody[protocol.AdminChannelList](t, rec)
	byName := map[string]protocol.Channel{}
	for _, c := range list.Items {
		if c.Kind != protocol.ChannelPublic && c.Kind != protocol.ChannelPrivate {
			t.Errorf("DM listed: %+v", c)
		}
		if c.Name != nil {
			byName[*c.Name] = c
		}
	}
	if c := byName["secret-plans"]; c.ID != secret.ID || c.MemberCount != 2 || c.Kind != protocol.ChannelPrivate {
		t.Errorf("private channel = %+v", c)
	}
	if byName["town-square"].ID != pub.ID || byName["general"].ID == "" {
		t.Errorf("channels = %v", byName)
	}

	// No admin endpoint carries message content, and the ordinary message
	// endpoints stay closed to a non-member admin.
	for _, p := range []string{"/overview", "/channels", "/audit?limit=100", "/users", "/invites", "/settings", "/apps", "/webhooks", "/reset/preview", "/tls"} {
		if got := admin.do(http.MethodGet, adminAPIPath+p, nil).Body.String(); strings.Contains(got, body) {
			t.Errorf("GET /admin%s returned message content", p)
		}
	}
	for _, id := range []string{secret.ID, dmCh.ID} {
		wantStatus(t, admin.do(http.MethodGet, "/api/v1/channels/"+id+"/messages", nil), http.StatusNotFound, CodeNotFound)
		wantStatus(t, admin.do(http.MethodGet, "/api/v1/channels/"+id+"/pins", nil), http.StatusNotFound, CodeNotFound)
	}
	rec = admin.do(http.MethodGet, "/api/v1/search?q=launch", nil)
	wantStatus(t, rec, http.StatusOK, "")
	if strings.Contains(rec.Body.String(), body) {
		t.Error("search returned content of a channel the admin is not in")
	}

	// PATCH works without membership and is audited; DMs are not reachable.
	rec = admin.do(http.MethodPatch, adminAPIPath+"/channels/"+secret.ID, `{"retention_days":14,"archived":true}`)
	wantStatus(t, rec, http.StatusOK, "")
	got := decodeBody[protocol.Channel](t, rec)
	if got.RetentionDays == nil || *got.RetentionDays != 14 || got.ArchivedAt == nil {
		t.Errorf("patched = %+v", got)
	}
	rec = admin.do(http.MethodPatch, adminAPIPath+"/channels/"+secret.ID, `{"retention_days":null,"archived":false}`)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.Channel](t, rec); got.RetentionDays != nil || got.ArchivedAt != nil {
		t.Errorf("reverted = %+v", got)
	}
	rec = admin.do(http.MethodPatch, adminAPIPath+"/channels/"+pub.ID, `{"is_default":true}`)
	wantStatus(t, rec, http.StatusOK, "")
	if !decodeBody[protocol.Channel](t, rec).IsDefault {
		t.Error("default flag not set")
	}
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/channels/"+secret.ID, `{"is_default":true}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/channels/"+pub.ID, `{"retention_days":0}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/channels/"+pub.ID, `{"retention_days":"soon"}`), http.StatusBadRequest, CodeBadRequest)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/channels/"+dmCh.ID, `{"archived":true}`), http.StatusNotFound, CodeNotFound)
	wantStatus(t, admin.do(http.MethodPatch, adminAPIPath+"/channels/nope", `{"archived":true}`), http.StatusNotFound, CodeNotFound)
	rec = admin.do(http.MethodGet, adminAPIPath+"/audit?action=channel.admin_update", nil)
	if n := len(decodeBody[protocol.AuditPage](t, rec).Items); n != 3 {
		t.Errorf("channel audit rows = %d", n)
	}
}

func TestAdminTLS(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()

	rec := admin.do(http.MethodGet, adminAPIPath+"/tls", nil)
	wantStatus(t, rec, http.StatusOK, "")
	st := decodeBody[protocol.AdminTLS](t, rec)
	if st.Mode != "local-ca" || st.CAFingerprint != "AB:CD" || len(st.SANs) != 2 || st.LeafNotAfter == "" {
		t.Errorf("tls = %+v", st)
	}
	wantStatus(t, admin.do(http.MethodPost, adminAPIPath+"/tls/reissue", nil), http.StatusOK, "")
	rec = admin.do(http.MethodGet, adminAPIPath+"/audit?action=tls.reissue", nil)
	if n := len(decodeBody[protocol.AuditPage](t, rec).Items); n != 1 {
		t.Errorf("reissue audit rows = %d", n)
	}
}
