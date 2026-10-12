package main

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/auth"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// resetDataDir is a data directory with one message, a CA, web reset turned
// off and the instance name "Party".
func resetDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st := settings.New(db)
	for key, v := range map[string]any{settings.KeyInstanceName: "Party", settings.KeyWebResetEnabled: false} {
		if err := st.Set(ctx, audit.Console, key, v); err != nil {
			t.Fatal(err)
		}
	}
	err = db.Do(ctx, func(tx *store.Tx) error {
		for _, q := range []string{
			`INSERT INTO users (id, username, kind, role, created_at) VALUES ('u1', 'owner', 'human', 'admin', 1)`,
			`INSERT INTO channels (id, kind, name, created_at) VALUES ('c1', 'public', 'general', 1)`,
			`INSERT INTO messages (id, channel_id, author_id, body, body_plain, created_at) VALUES ('m1', 'c1', 'u1', 'hi', 'hi', 1)`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	ca, err := tlsca.NewCA(tlsca.CAOptions{InstanceName: "party", Hostname: "host", ExtraDNS: []string{"chat.example.lan"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Save(filepath.Join(dir, config.DirTLS)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runResetCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	appOpts := app.Options{
		Hostname: func() (string, error) { return "host", nil },
		LANAddrs: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.1.20")}, nil },
	}
	code := resetCmd(context.Background(), args, config.Options{Getenv: func(string) string { return "" }}, appOpts,
		strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func countRows(t *testing.T, dir, query string) int {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.Read().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestResetCLIUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--content", "--factory"},
		{"--content", "--rotate-ca"},
		{"--content", "--keep-settings"},
		{"--factory", "extra"},
		{"--wipe"},
	} {
		if code, _, _ := runResetCLI(t, "", append(args, "--data-dir", t.TempDir())...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, _ := runResetCLI(t, "", "--content", "--data-dir", t.TempDir()); code != exitRuntime {
		t.Errorf("empty data dir: exit %d", code)
	}
}

func TestResetCLIRefusesWhileServerRuns(t *testing.T) {
	dir := resetDataDir(t)
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	code, _, stderr := runResetCLI(t, "", "--factory", "--yes", "--data-dir", dir)
	if code != exitDirInUse || !strings.Contains(stderr, "running") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := countRows(t, dir, `SELECT count(*) FROM messages`); n != 1 {
		t.Error("a refused reset deleted data")
	}
}

// TestResetCLIWorksWhenWebResetIsDisabled: the console needs the typed
// instance name, and then clears content whatever web_reset_enabled says.
func TestResetCLIWorksWhenWebResetIsDisabled(t *testing.T) {
	dir := resetDataDir(t)
	code, stdout, stderr := runResetCLI(t, "party\n", "--content", "--data-dir", dir)
	if code != exitRuntime || !strings.Contains(stderr, "does not match") || !strings.Contains(stdout, "1 messages") {
		t.Fatalf("wrong confirmation: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if n := countRows(t, dir, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatal("a wrong confirmation deleted data")
	}
	code, stdout, stderr = runResetCLI(t, "Party\n", "--content", "--data-dir", dir)
	if code != exitOK || !strings.Contains(stdout, "epoch 2") {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	if n := countRows(t, dir, `SELECT count(*) FROM messages`); n != 0 {
		t.Errorf("%d messages after clear", n)
	}
	if n := countRows(t, dir, `SELECT count(*) FROM audit_log WHERE action = 'system.content_cleared' AND actor_kind = 'console'`); n != 1 {
		t.Error("no console audit row")
	}
}

func TestResetCLIFactory(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		dir := resetDataDir(t)
		oldCA, err := tlsca.LoadCA(filepath.Join(dir, config.DirTLS))
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--factory", "--keep-settings", "--yes", "--data-dir", dir}
		if rotate {
			args = append(args, "--rotate-ca")
		}
		code, stdout, stderr := runResetCLI(t, "", args...)
		if code != exitOK {
			t.Fatalf("rotate=%v: exit %d\n%s\n%s", rotate, code, stdout, stderr)
		}
		if !strings.Contains(stdout, "it prints a new admin password") {
			t.Errorf("no hint about the new admin password:\n%s", stdout)
		}
		if _, err := os.Stat(filepath.Join(dir, auth.AdminPasswordFile)); !os.IsNotExist(err) {
			t.Errorf("admin password file after the offline reset: %v", err)
		}
		ca, err := tlsca.LoadCA(filepath.Join(dir, config.DirTLS))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout, ca.Fingerprint()) {
			t.Errorf("fingerprint not printed:\n%s", stdout)
		}
		if changed := ca.Fingerprint() != oldCA.Fingerprint(); changed != rotate {
			t.Errorf("rotate=%v but CA changed=%v", rotate, changed)
		}
		if rotate && !strings.Contains(strings.Join(ca.Cert.PermittedDNSDomains, ","), "chat.example.lan") {
			t.Errorf("new CA lost the extra DNS name: %v", ca.Cert.PermittedDNSDomains)
		}
		if n := countRows(t, dir, `SELECT count(*) FROM users`); n != 0 {
			t.Errorf("%d users after factory reset", n)
		}
		if n := countRows(t, dir, `SELECT count(*) FROM settings WHERE key = 'instance_name' AND value = '"Party"'`); n != 1 {
			t.Error("--keep-settings lost the instance name")
		}
	}
}
