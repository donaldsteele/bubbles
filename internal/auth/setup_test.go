package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/store"
)

func goodSetup(token string) SetupInput {
	return SetupInput{
		Token: token, Username: "Admin", Password: goodPassword, DisplayName: "The Admin",
		InstanceName: "Home Chat", UserAgent: "test-agent", IP: "192.168.1.2",
	}
}

func TestInitSetupWritesToken(t *testing.T) {
	e := newEnv(t)
	token, err := e.svc.InitSetup(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 52 || strings.Trim(token, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Errorf("token = %q, want 52 base32 characters", token)
	}
	path := filepath.Join(e.dataDir, SetupTokenFile)
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != token {
		t.Fatalf("token file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
		}
	}
	// Only the hash is kept in memory.
	if string(e.svc.setupHash) == token || len(e.svc.setupHash) != 32 {
		t.Error("service keeps something other than the SHA-256")
	}

	// A restart before setup replaces the token (and the file).
	token2, err := e.svc.InitSetup(e.ctx)
	if err != nil || token2 == token {
		t.Fatalf("second InitSetup = %q, %v", token2, err)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != token2 {
		t.Error("token file not replaced")
	}
	if e.svc.setupTokenValid(token) || !e.svc.setupTokenValid(token2) {
		t.Error("old token still valid or new token invalid")
	}
}

func TestSetupRejectsMissingAndWrongToken(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	for _, bad := range []string{"", "wrong", strings.Repeat("a", 52), token + "x"} {
		if _, err := e.svc.Setup(e.ctx, goodSetup(bad)); !errors.Is(err, ErrSetupToken) {
			t.Errorf("token %q: err = %v, want ErrSetupToken", bad, err)
		}
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 0 {
		t.Errorf("%d users created by rejected setups", n)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, SetupTokenFile)); err != nil {
		t.Errorf("token file removed by failed setup: %v", err)
	}
}

func TestSetupWrongTokenIsRateLimited(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	for i := 0; i < LoginIPMaxFailures; i++ {
		if _, err := e.svc.Setup(e.ctx, goodSetup("guess")); !errors.Is(err, ErrSetupToken) {
			t.Fatalf("guess %d: %v", i, err)
		}
	}
	var rl *RateLimitedError
	if _, err := e.svc.Setup(e.ctx, goodSetup(token)); !errors.As(err, &rl) {
		t.Errorf("after 20 wrong tokens from one IP: %v", err)
	}
}

func TestSetupWorksExactlyOnce(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)

	res, err := e.svc.Setup(e.ctx, goodSetup("  "+strings.ToUpper(token)+"\n"))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if res.User.Username != "admin" || res.User.Role != RoleAdmin || res.User.DisplayName != "The Admin" || res.Token == "" {
		t.Errorf("result = %+v / %+v", res.User, res)
	}
	if got := e.settings.InstanceName(); got != "Home Chat" {
		t.Errorf("instance_name = %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, SetupTokenFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file still exists: %v", err)
	}
	if _, err := e.svc.Authenticate(e.ctx, res.Token); err != nil {
		t.Errorf("setup session invalid: %v", err)
	}
	if _, err := e.login("admin", goodPassword); err != nil {
		t.Errorf("admin cannot log in: %v", err)
	}
	if e.auditCount(audit.ActionSetup) != 1 || e.auditCount(audit.ActionSettingsUpdated) != 1 {
		t.Errorf("audit rows: setup=%d settings=%d", e.auditCount(audit.ActionSetup), e.auditCount(audit.ActionSettingsUpdated))
	}

	// Second run: even the old token no longer works.
	second := goodSetup(token)
	second.Username = "mallory"
	if _, err := e.svc.Setup(e.ctx, second); !errors.Is(err, ErrSetupDone) {
		t.Errorf("second Setup = %v, want ErrSetupDone", err)
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 1 {
		t.Errorf("%d users", n)
	}
	if needs, _ := e.svc.NeedsSetup(e.ctx); needs {
		t.Error("NeedsSetup after setup")
	}
}

func TestSetupConcurrentRequestsCreateOneAdmin(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := goodSetup(token)
			in.Username = "admin" + string(rune('a'+i))
			_, errs[i] = e.svc.Setup(e.ctx, in)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrSetupDone):
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d setups succeeded, want exactly 1", ok)
	}
	if c, _ := e.svc.CountUsers(e.ctx); c != 1 {
		t.Errorf("%d users", c)
	}
}

func TestSetupValidation(t *testing.T) {
	e := newEnv(t)
	token, _ := e.svc.InitSetup(e.ctx)
	in := goodSetup(token)
	in.Username, in.Password, in.InstanceName, in.DisplayName = "A", "short", "  ", strings.Repeat("x", 65)
	var ve *ValidationError
	_, err := e.svc.Setup(e.ctx, in)
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	for _, f := range []string{"username", "password", "instance_name", "display_name"} {
		if ve.Fields[f] == "" {
			t.Errorf("no reason for field %s: %v", f, ve.Fields)
		}
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 0 {
		t.Error("user created despite validation errors")
	}
	in = goodSetup(token)
	in.Password = "1234567890"
	if _, err := e.svc.Setup(e.ctx, in); !errors.As(err, &ve) || ve.Fields["password"] == "" {
		t.Errorf("common password accepted: %v", err)
	}
}

func TestSetupHook(t *testing.T) {
	var gotAdmin string
	e := newEnv(t, envOpts{onSetup: func(_ context.Context, tx *store.Tx, adminID string) error {
		gotAdmin = adminID
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('hook_ran', 'true')`)
		return err
	}})
	token, _ := e.svc.InitSetup(e.ctx)
	res, err := e.svc.Setup(e.ctx, goodSetup(token))
	if err != nil {
		t.Fatal(err)
	}
	if gotAdmin != res.User.ID {
		t.Errorf("hook admin id = %q, want %q", gotAdmin, res.User.ID)
	}
	if e.count(`SELECT COUNT(*) FROM settings WHERE key = 'hook_ran'`) != 1 {
		t.Error("hook write missing")
	}
}

func TestSetupHookFailureRollsBackEverything(t *testing.T) {
	boom := errors.New("boom")
	e := newEnv(t, envOpts{onSetup: func(context.Context, *store.Tx, string) error { return boom }})
	token, _ := e.svc.InitSetup(e.ctx)
	if _, err := e.svc.Setup(e.ctx, goodSetup(token)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 0 {
		t.Error("admin created although the hook failed")
	}
	if e.count(`SELECT COUNT(*) FROM settings`)+e.count(`SELECT COUNT(*) FROM sessions`) != 0 {
		t.Error("partial setup committed")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, SetupTokenFile)); err != nil {
		t.Error("token file removed although setup failed")
	}
	// The token still works once the problem is fixed.
	e.svc.onSetup = nil
	if _, err := e.svc.Setup(e.ctx, goodSetup(token)); err != nil {
		t.Errorf("retry: %v", err)
	}
}

func TestInitSetupWithExistingUsersRemovesStaleToken(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleAdmin)
	stale := filepath.Join(e.dataDir, SetupTokenFile)
	if err := os.WriteFile(stale, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := e.svc.InitSetup(e.ctx)
	if err != nil || token != "" {
		t.Fatalf("InitSetup = %q, %v", token, err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale token file remains: %v", err)
	}
}
