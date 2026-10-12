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

func TestGeneratePasswordFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pw, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 19 {
			t.Fatalf("password %q has %d characters, want 19", pw, len(pw))
		}
		for j, c := range pw {
			if j%5 == 4 {
				if c != '-' {
					t.Fatalf("password %q: want '-' at %d", pw, j)
				}
			} else if !strings.ContainsRune(passwordAlphabet, c) {
				t.Fatalf("password %q: %q is outside the alphabet", pw, c)
			}
		}
		if seen[pw] {
			t.Fatalf("password %q generated twice", pw)
		}
		seen[pw] = true
	}
}

func TestBootstrapAdminCreatesAdminOnce(t *testing.T) {
	e := newEnv(t)
	pw, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil || len(pw) != 19 {
		t.Fatalf("BootstrapAdmin = %q, %v", pw, err)
	}
	res, err := e.login("admin", pw)
	if err != nil {
		t.Fatalf("admin cannot log in: %v", err)
	}
	if u := res.User; u.Username != "admin" || u.DisplayName != "Admin" || u.Role != RoleAdmin || u.Kind != KindHuman {
		t.Errorf("admin = %+v", u)
	}
	if e.auditCount(audit.ActionSetup) != 1 {
		t.Errorf("setup audit rows = %d, want 1", e.auditCount(audit.ActionSetup))
	}
	if e.count(`SELECT COUNT(*) FROM audit_log WHERE action = ? AND actor_kind = ?`, audit.ActionSetup, audit.KindConsole) != 1 {
		t.Error("setup audit row is not by the console actor")
	}

	path := filepath.Join(e.dataDir, AdminPasswordFile)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# bubbles admin sign-in. Deleted when the admin password changes.\nusername: admin\npassword: " + pw + "\n"
	if string(b) != want {
		t.Errorf("file = %q, want %q", b, want)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
		}
	}

	// A later start does nothing: no new password, the file stays.
	again, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil || again != "" {
		t.Errorf("second BootstrapAdmin = %q, %v", again, err)
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 1 {
		t.Errorf("%d users", n)
	}
	if b2, _ := os.ReadFile(path); string(b2) != want {
		t.Error("password file changed by the second call")
	}
}

func TestBootstrapAdminUsesFixedPasswordAndNoFileWithoutDataDir(t *testing.T) {
	e := newEnv(t)
	e.svc.adminPassword = "fixed-test-password"
	e.svc.dataDir = ""
	pw, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil || pw != "fixed-test-password" {
		t.Fatalf("BootstrapAdmin = %q, %v", pw, err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, AdminPasswordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file written without a data directory: %v", err)
	}
}

func TestBootstrapAdminReplacesStaleFile(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(e.dataDir, AdminPasswordFile)
	if err := os.WriteFile(path, []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pw, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), pw) {
		t.Errorf("stale file not replaced: %q", b)
	}
}

func TestBootstrapAdminWithExistingUsersDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.addUser("alice", RoleAdmin)
	pw, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil || pw != "" {
		t.Fatalf("BootstrapAdmin = %q, %v", pw, err)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, AdminPasswordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file written: %v", err)
	}
}

func TestBootstrapAdminConcurrentCallsCreateOneAdmin(t *testing.T) {
	e := newEnv(t)
	const n = 6
	pws := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pws[i], errs[i] = e.svc.BootstrapAdmin(e.ctx)
		}()
	}
	wg.Wait()
	winners := 0
	for i := range pws {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
		}
		if pws[i] != "" {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("%d calls returned a password, want exactly 1", winners)
	}
	if c, _ := e.svc.CountUsers(e.ctx); c != 1 {
		t.Errorf("%d users", c)
	}
}

func TestBootstrapAdminHook(t *testing.T) {
	var gotAdmin string
	e := newEnv(t, envOpts{onBootstrap: func(_ context.Context, tx *store.Tx, adminID string) error {
		gotAdmin = adminID
		_, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('hook_ran', 'true')`)
		return err
	}})
	pw, err := e.svc.BootstrapAdmin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.login("admin", pw)
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

func TestBootstrapAdminHookFailureRollsBackEverything(t *testing.T) {
	boom := errors.New("boom")
	e := newEnv(t, envOpts{onBootstrap: func(context.Context, *store.Tx, string) error { return boom }})
	if _, err := e.svc.BootstrapAdmin(e.ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n, _ := e.svc.CountUsers(e.ctx); n != 0 {
		t.Error("admin created although the hook failed")
	}
	if e.auditCount(audit.ActionSetup) != 0 {
		t.Error("audit row committed although the hook failed")
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, AdminPasswordFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file written although bootstrap failed: %v", err)
	}
	// Once the problem is fixed the next start succeeds.
	e.svc.onBootstrap = nil
	if pw, err := e.svc.BootstrapAdmin(e.ctx); err != nil || pw == "" {
		t.Errorf("retry = %q, %v", pw, err)
	}
}

func TestAdminPasswordFileDeletedWhenAdminPasswordChanges(t *testing.T) {
	exists := func(e *testEnv) bool {
		_, err := os.Stat(filepath.Join(e.dataDir, AdminPasswordFile))
		return err == nil
	}

	t.Run("change password", func(t *testing.T) {
		e := newEnv(t)
		pw, _ := e.svc.BootstrapAdmin(e.ctx)
		res, err := e.login("admin", pw)
		if err != nil {
			t.Fatal(err)
		}
		// A wrong current password changes nothing.
		bad := ChangePasswordInput{UserID: res.User.ID, Current: "nope", New: "a brand new passphrase"}
		if err := e.svc.ChangePassword(e.ctx, bad); !errors.Is(err, ErrWrongPassword) || !exists(e) {
			t.Fatalf("wrong current: %v, file exists %v", err, exists(e))
		}
		err = e.svc.ChangePassword(e.ctx, ChangePasswordInput{UserID: res.User.ID, Current: pw, New: "a brand new passphrase"})
		if err != nil {
			t.Fatal(err)
		}
		if exists(e) {
			t.Error("admin-password.txt survived the password change")
		}
	})
	t.Run("reset link", func(t *testing.T) {
		e := newEnv(t)
		pw, _ := e.svc.BootstrapAdmin(e.ctx)
		res, _ := e.login("admin", pw)
		code, _, err := e.svc.CreateReset(e.ctx, audit.Console, res.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !exists(e) {
			t.Fatal("creating a reset link must not delete the file")
		}
		if err := e.svc.ResetPassword(e.ctx, code, "a brand new passphrase", ""); err != nil {
			t.Fatal(err)
		}
		if exists(e) {
			t.Error("admin-password.txt survived the reset")
		}
	})
	t.Run("other users", func(t *testing.T) {
		e := newEnv(t)
		if _, err := e.svc.BootstrapAdmin(e.ctx); err != nil {
			t.Fatal(err)
		}
		alice := e.addUser("alice", RoleMember)
		err := e.svc.ChangePassword(e.ctx, ChangePasswordInput{UserID: alice.ID, Current: goodPassword, New: "a brand new passphrase"})
		if err != nil {
			t.Fatal(err)
		}
		code, _, _ := e.svc.CreateReset(e.ctx, audit.Console, alice.ID)
		if err := e.svc.ResetPassword(e.ctx, code, "another new passphrase", ""); err != nil {
			t.Fatal(err)
		}
		if !exists(e) {
			t.Error("another user's password change deleted admin-password.txt")
		}
	})
}
