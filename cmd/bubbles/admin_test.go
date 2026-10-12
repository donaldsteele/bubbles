package main

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

var adminNow = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func runAdminTest(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	opts := config.Options{Getenv: func(k string) string { return env[k] }}
	appOpts := app.Options{
		Now: func() time.Time { return adminNow },
		LANAddrs: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("fe80::1"), netip.MustParseAddr("192.168.7.7")}, nil
		},
	}
	code = adminCmd(args, opts, appOpts, &out, &errb)
	return code, out.String(), errb.String()
}

// openDB opens the data directory's database the way a second process would.
func openDB(t *testing.T, dir string) (*store.DB, *auth.Service) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{Now: func() time.Time { return adminNow }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	svc, err := auth.New(auth.Options{DB: db, Settings: settings.New(db), Now: func() time.Time { return adminNow }, Params: auth.Params{Time: 1, MemoryKiB: 8, Threads: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	return db, svc
}

var urlCode = regexp.MustCompile(`^https://192\.168\.7\.7:(\d+)/(invite|reset)/([a-z2-7]{52})\n$`)

func TestAdminUsageErrors(t *testing.T) {
	dir := t.TempDir()
	tests := [][]string{
		nil,
		{"nope"},
		{"reset-password", "--data-dir", dir},
		{"reset-password", "a", "b", "--data-dir", dir},
		{"list-users", "extra", "--data-dir", dir},
		{"create-invite", "--data-dir", dir, "--role", "root"},
		{"create-invite", "--data-dir", dir, "--expires", "soon"},
		{"create-invite", "--data-dir", dir, "--expires", "0s"},
		{"create-invite", "--data-dir", dir, "--expires", "9999h"},
		{"create-invite", "--data-dir", dir, "--uses", "0"},
		{"create-invite", "--data-dir", dir, "--no-such-flag"},
	}
	for _, args := range tests {
		if code, stdout, _ := runAdminTest(t, nil, args...); code != exitUsage || stdout != "" {
			t.Errorf("admin %q: exit = %d, stdout = %q; want %d and no output", args, code, stdout, exitUsage)
		}
	}
	if code, stdout, _ := runAdminTest(t, nil, "--help"); code != exitOK || !strings.Contains(stdout, "create-invite") {
		t.Errorf("--help: exit = %d, stdout = %q", code, stdout)
	}
}

func TestAdminListUsersOnFreshDir(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runAdminTest(t, nil, "list-users", "--data-dir", dir)
	if code != exitOK || stderr != "" || !strings.HasPrefix(stdout, "USERNAME") || strings.Count(stdout, "\n") != 1 {
		t.Errorf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
		t.Errorf("database not created: %v", err)
	}
}

func TestAdminMissingDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo")
	code, _, stderr := runAdminTest(t, nil, "list-users", "--data-dir", dir)
	if code != exitRuntime || !strings.Contains(stderr, "does not exist") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("a mistyped --data-dir was created (%v)", err)
	}
}

func TestAdminDataDirFromEnv(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runAdminTest(t, map[string]string{"BUBBLES_DATA_DIR": dir}, "create-invite")
	if code != exitOK || !urlCode.MatchString(stdout) {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileName)); err != nil {
		t.Errorf("BUBBLES_DATA_DIR ignored: %v", err)
	}
}

func TestAdminCreateInvite(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runAdminTest(t, nil, "create-invite", "--data-dir", dir, "--role", "admin", "--expires", "1h", "--uses", "3", "--https-addr", ":9443")
	m := urlCode.FindStringSubmatch(stdout)
	if code != exitOK || m == nil {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if m[1] != "9443" || m[2] != "invite" {
		t.Errorf("url = %q, want port 9443 and /invite/", stdout)
	}
	if strings.Contains(stderr, m[3]) {
		t.Error("the code was printed to stderr as well")
	}

	db, svc := openDB(t, dir)
	st, err := svc.LookupInvite(context.Background(), m[3])
	if err != nil || !st.Valid || st.Role != auth.RoleAdmin {
		t.Fatalf("LookupInvite = %+v, %v", st, err)
	}
	var maxUses int
	var expires int64
	if err := db.Read().QueryRow(`SELECT max_uses, expires_at FROM invites`).Scan(&maxUses, &expires); err != nil {
		t.Fatal(err)
	}
	if maxUses != 3 || expires != adminNow.Add(time.Hour).UnixMilli() {
		t.Errorf("max_uses = %d, expires_at = %d", maxUses, expires)
	}
	// The invite works, and the console action is audited without the code.
	if _, err := svc.AcceptInvite(context.Background(), auth.AcceptInviteInput{Code: m[3], Username: "root2", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	var kind, actorID, detail string
	err = db.Read().QueryRow(`SELECT actor_kind, COALESCE(actor_id, ''), detail FROM audit_log WHERE action = ?`, audit.ActionInviteCreate).Scan(&kind, &actorID, &detail)
	if err != nil || kind != "console" || actorID != "" || strings.Contains(detail, m[3]) {
		t.Errorf("audit row: kind=%q actor=%q detail=%q err=%v", kind, actorID, detail, err)
	}
}

func TestAdminResetPassword(t *testing.T) {
	dir := t.TempDir()
	_, stdout, _ := runAdminTest(t, nil, "create-invite", "--data-dir", dir)
	invite := urlCode.FindStringSubmatch(stdout)
	db, svc := openDB(t, dir)
	ctx := context.Background()
	if _, err := svc.AcceptInvite(ctx, auth.AcceptInviteInput{Code: invite[3], Username: "alice", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}

	// Flags may follow the username; the lookup is case-insensitive.
	code, stdout, stderr := runAdminTest(t, nil, "reset-password", "ALICE", "--data-dir", dir)
	m := urlCode.FindStringSubmatch(stdout)
	if code != exitOK || m == nil || m[2] != "reset" || m[1] != "8443" {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	if err := svc.ResetPassword(ctx, m[3], "a brand new passphrase", ""); err != nil {
		t.Fatalf("the printed link does not work: %v", err)
	}
	if _, err := svc.Login(ctx, auth.LoginInput{Username: "alice", Password: "a brand new passphrase"}); err != nil {
		t.Errorf("login with the reset password: %v", err)
	}
	var kind string
	if err := db.Read().QueryRow(`SELECT actor_kind FROM audit_log WHERE action = ?`, audit.ActionResetCreate).Scan(&kind); err != nil || kind != "console" {
		t.Errorf("audit actor_kind = %q, %v", kind, err)
	}

	if code, stdout, stderr := runAdminTest(t, nil, "reset-password", "ghost", "--data-dir", dir); code != exitRuntime || stdout != "" || !strings.Contains(stderr, "no user") {
		t.Errorf("unknown user: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
}

func TestAdminListUsers(t *testing.T) {
	dir := t.TempDir()
	_, stdout, _ := runAdminTest(t, nil, "create-invite", "--data-dir", dir, "--role", "guest")
	invite := urlCode.FindStringSubmatch(stdout)
	_, svc := openDB(t, dir)
	if _, err := svc.AcceptInvite(context.Background(), auth.AcceptInviteInput{Code: invite[3], Username: "gus", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runAdminTest(t, nil, "list-users", "--data-dir", dir)
	if code != exitOK || !regexp.MustCompile(`(?m)^gus\s+guest\s+human\s+active\s+2026-03-01T12:00:00\.000Z$`).MatchString(out) {
		t.Errorf("exit = %d, output:\n%s", code, out)
	}
}

func TestAdminDispatch(t *testing.T) {
	if _, ok := commands["admin"]; !ok {
		t.Fatal("admin is not registered")
	}
	var stderr bytes.Buffer
	if code := dispatch([]string{"admin"}, &stderr); code != exitUsage {
		t.Errorf("exit = %d", code)
	}
}
