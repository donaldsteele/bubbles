package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/backup"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// fakeResetter stands in for the composition root: it checks the
// confirmation against "Home" (the instance name e.setup picks) and the
// password against goodPassword, records the calls and streams real backups.
type fakeResetter struct {
	dataDir string
	db      *store.DB

	mu        sync.Mutex
	calls     []string
	inputs    []ResetInput
	err       error // returned by the next reset or backup instead
	backupErr error
}

func newFakeResetter(dir string, db *store.DB) *fakeResetter {
	return &fakeResetter{dataDir: dir, db: db}
}

func (f *fakeResetter) record(call string, in ResetInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return f.err
	}
	if strings.TrimSpace(in.Confirm) != "Home" {
		return reset.ErrConfirm
	}
	if in.Password != goodPassword {
		return reset.ErrPassword
	}
	return nil
}

func (f *fakeResetter) snapshot() ([]string, []ResetInput) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]ResetInput(nil), f.inputs...)
}

func (f *fakeResetter) ResetPreview(ctx context.Context) (protocol.ResetPreview, error) {
	return reset.Preview(ctx, f.db)
}

func (f *fakeResetter) ClearContent(_ context.Context, in ResetInput) (protocol.ContentCleared, error) {
	if err := f.record("content", in); err != nil {
		return protocol.ContentCleared{}, err
	}
	return protocol.ContentCleared{Epoch: 2}, nil
}

func (f *fakeResetter) FactoryReset(_ context.Context, in ResetInput) (protocol.FactoryReset, error) {
	if err := f.record("factory", in); err != nil {
		return protocol.FactoryReset{}, err
	}
	return protocol.FactoryReset{
		SetupToken: "newtoken", SetupURL: "https://192.168.1.20:8443/",
		CARotated: in.RotateCA, CAFingerprint: "sha256:ab",
	}, nil
}

func (f *fakeResetter) Backup(ctx context.Context, w io.Writer, passphrase string) error {
	f.mu.Lock()
	berr := f.backupErr
	f.mu.Unlock()
	if berr != nil {
		return berr
	}
	return backup.Create(ctx, f.dataDir, w, backup.CreateOptions{Passphrase: passphrase, WorkFactor: 10})
}

func resetBody(mode, confirm, password string) protocol.ResetRequest {
	return protocol.ResetRequest{Mode: mode, Confirm: confirm, Password: password}
}

func TestAdminResetPreviewCounts(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	ch := admin.createChannel(t, "talk", protocol.ChannelPublic)
	admin.post(t, ch.ID, "hello")

	rec := admin.do(http.MethodGet, "/api/v1/admin/reset/preview", nil)
	wantStatus(t, rec, http.StatusOK, "")
	p := decodeBody[protocol.ResetPreview](t, rec)
	if p.Users != 1 || p.Channels < 2 || p.Messages != 1 || p.Files != 0 || p.Bytes != 0 {
		t.Errorf("preview = %+v", p)
	}
}

func TestAdminResetRunsBothModes(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()

	rec := admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetContent, " Home ", goodPassword))
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.ContentCleared](t, rec); got.Epoch != 2 {
		t.Errorf("content answer = %+v", got)
	}
	req := resetBody(protocol.ResetFactory, "Home", goodPassword)
	req.RotateCA, req.KeepSettings = true, true
	rec = admin.do(http.MethodPost, "/api/v1/admin/reset", req)
	wantStatus(t, rec, http.StatusOK, "")
	if got := decodeBody[protocol.FactoryReset](t, rec); got.SetupToken != "newtoken" || !got.CARotated || got.CAFingerprint == "" || got.SetupURL == "" {
		t.Errorf("factory answer = %+v", got)
	}
	calls, inputs := e.reset.snapshot()
	if strings.Join(calls, ",") != "content,factory" {
		t.Fatalf("calls = %v", calls)
	}
	in := inputs[1]
	if in.Actor.ID != admin.id || in.Actor.Kind != audit.KindUser || in.Actor.IP == "" || !in.RotateCA || !in.KeepSettings {
		t.Errorf("factory input = %+v", in)
	}
}

func TestAdminResetWrongConfirmOrPasswordIs403(t *testing.T) {
	for _, tc := range []struct{ name, confirm, password, msg string }{
		{"confirm", "home", goodPassword, "instance name"},
		{"password", "Home", "not the password", "password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			admin := e.newAdmin()
			rec := admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetFactory, tc.confirm, tc.password))
			if got := wantStatus(t, rec, http.StatusForbidden, CodeForbidden); !strings.Contains(got.Message, tc.msg) {
				t.Errorf("message = %q", got.Message)
			}
		})
	}
}

func TestAdminResetFourthAttemptInAnHourIs429(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	for i := range ResetAttemptsPerHour {
		rec := admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetContent, "wrong", goodPassword))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	// The right answer does not help once the allowance is used up.
	rec := admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetContent, "Home", goodPassword))
	wantStatus(t, rec, http.StatusTooManyRequests, CodeRateLimited)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if calls, _ := e.reset.snapshot(); len(calls) != ResetAttemptsPerHour {
		t.Errorf("resetter called %d times", len(calls))
	}
	e.clock.Advance(21 * time.Minute)
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetContent, "Home", goodPassword)), http.StatusOK, "")
}

func TestAdminResetValidation(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	for name, tc := range map[string]struct {
		body  any
		field string
	}{
		"bad mode":            {resetBody("everything", "Home", goodPassword), "mode"},
		"rotate with content": {protocol.ResetRequest{Mode: protocol.ResetContent, Confirm: "Home", Password: goodPassword, RotateCA: true}, "rotate_ca"},
		"keep with content":   {protocol.ResetRequest{Mode: protocol.ResetContent, Confirm: "Home", Password: goodPassword, KeepSettings: true}, "keep_settings"},
		"no confirm":          {resetBody(protocol.ResetFactory, "", goodPassword), "confirm"},
		"no password":         {resetBody(protocol.ResetFactory, "Home", ""), "password"},
		"unknown field":       {`{"mode":"content","confirm":"Home","password":"x","wipe":true}`, "wipe"},
	} {
		t.Run(name, func(t *testing.T) {
			e.clock.Advance(time.Hour) // keep the attempt allowance fresh
			got := wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/reset", tc.body), http.StatusBadRequest, CodeBadRequest)
			if fields, _ := got.Details["fields"].(map[string]any); fields[tc.field] == nil {
				t.Errorf("details = %v, want field %q", got.Details, tc.field)
			}
		})
	}
	if calls, _ := e.reset.snapshot(); len(calls) != 0 {
		t.Errorf("resetter called: %v", calls)
	}
}

func TestAdminResetIsAdminAndBrowserSessionOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	member := e.newMember("mia", "192.168.1.51")
	guest := e.newGuest("gus", "192.168.1.52")
	b := e.newBot(admin, "bot", protocol.AppScopes, nil)
	anon := e.client("192.168.1.53")
	body := resetBody(protocol.ResetFactory, "Home", goodPassword)
	crossOrigin := func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }

	for _, op := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/admin/reset/preview", nil},
		{http.MethodPost, "/api/v1/admin/reset", body},
		{http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{Passphrase: "pass phrase"}},
	} {
		t.Run(op.method+" "+op.path, func(t *testing.T) {
			wantStatus(t, anon.do(op.method, op.path, op.body), http.StatusUnauthorized, CodeUnauthorized)
			wantStatus(t, member.do(op.method, op.path, op.body), http.StatusForbidden, CodeForbidden)
			wantStatus(t, guest.do(op.method, op.path, op.body), http.StatusForbidden, CodeForbidden)
			wantStatus(t, b.do(e, op.method, op.path, op.body), http.StatusForbidden, CodeForbidden)
			wantStatus(t, anon.do(op.method, op.path, op.body, asBearer("bub_"+strings.Repeat("a", 52))), http.StatusUnauthorized, CodeUnauthorized)
			if op.method == http.MethodPost {
				wantStatus(t, admin.do(op.method, op.path, op.body, crossOrigin), http.StatusForbidden, CodeForbidden)
			}
		})
	}
	if calls, _ := e.reset.snapshot(); len(calls) != 0 {
		t.Errorf("resetter called: %v", calls)
	}
}

func TestAdminResetDisabledByWebResetSetting(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	if err := e.st.Set(e.ctx, audit.Console, settings.KeyWebResetEnabled, false); err != nil {
		t.Fatal(err)
	}
	got := wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetFactory, "Home", goodPassword)),
		http.StatusForbidden, CodeForbidden)
	if !strings.Contains(got.Message, "bubbles reset") {
		t.Errorf("message = %q", got.Message)
	}
	if calls, _ := e.reset.snapshot(); len(calls) != 0 {
		t.Errorf("resetter called: %v", calls)
	}
	// The refused attempt is audited with the reason "disabled".
	var detail string
	if err := e.db.Read().QueryRow(`SELECT detail FROM audit_log WHERE action = 'system.reset_denied'`).Scan(&detail); err != nil {
		t.Fatalf("no system.reset_denied audit row: %v", err)
	}
	if !strings.Contains(detail, `"reason":"disabled"`) || !strings.Contains(detail, `"mode":"factory"`) || strings.Contains(detail, goodPassword) {
		t.Errorf("audit detail = %s", detail)
	}
	// The preview (and the backup) stay available.
	wantStatus(t, admin.do(http.MethodGet, "/api/v1/admin/reset/preview", nil), http.StatusOK, "")
}

func TestAdminResetConflicts(t *testing.T) {
	for _, err := range []error{reset.ErrInProgress, reset.ErrPending} {
		e := newEnv(t)
		admin := e.newAdmin()
		e.reset.err = err
		wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/reset", resetBody(protocol.ResetContent, "Home", goodPassword)),
			http.StatusConflict, CodeConflict)
	}
}

func TestAdminBackupStreamsEncryptedArchive(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	rec := admin.do(http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{Passphrase: "backup pass phrase"})
	wantStatus(t, rec, http.StatusOK, "")
	h := rec.Header()
	if h.Get("Content-Type") != "application/octet-stream" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", h)
	}
	if cd := h.Get("Content-Disposition"); cd != `attachment; filename="bubbles-20260301-120000.bbk"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte("age-encryption.org/v1")) {
		t.Errorf("archive does not start with the age header: %q", rec.Body.Bytes()[:min(32, rec.Body.Len())])
	}
	id, err := age.NewScryptIdentity("backup pass phrase")
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(bytes.NewReader(rec.Body.Bytes()), id)
	if err != nil {
		t.Fatalf("decrypt archive: %v", err)
	}
	if plain, err := io.ReadAll(r); err != nil || len(plain) == 0 {
		t.Fatalf("archive content: %d bytes, %v", len(plain), err)
	}
}

func TestAdminBackupErrors(t *testing.T) {
	e := newEnv(t)
	admin := e.newAdmin()
	got := wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{}), http.StatusBadRequest, CodeBadRequest)
	if fields, _ := got.Details["fields"].(map[string]any); fields["passphrase"] == nil {
		t.Errorf("details = %v", got.Details)
	}
	// A failure before the first byte is still an ordinary JSON error.
	e.reset.backupErr = store.ErrClosed
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{Passphrase: "x"}),
		http.StatusServiceUnavailable, CodeUnavailable)
	e.reset.backupErr = errors.New("disk on fire")
	wantStatus(t, admin.do(http.MethodPost, "/api/v1/admin/backup", protocol.BackupRequest{Passphrase: "x"}),
		http.StatusInternalServerError, CodeInternal)
}
