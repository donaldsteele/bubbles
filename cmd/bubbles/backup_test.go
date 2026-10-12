package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/store"
)

func makeDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(filepath.Join(dir, "tls", "ca.crt"), []byte("CERT"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runBR(t *testing.T, f func(context.Context, []string, config.Options, *bytes.Buffer, *bytes.Buffer) int, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := f(context.Background(), args, config.Options{Getenv: func(string) string { return "" }}, &out, &errb)
	return code, errb.String()
}

func backupRun(t *testing.T, args ...string) (int, string) {
	return runBR(t, func(c context.Context, a []string, o config.Options, so, se *bytes.Buffer) int {
		return backupCmd(c, a, o, so, se)
	}, args...)
}

func restoreRun(t *testing.T, args ...string) (int, string) {
	return runBR(t, func(c context.Context, a []string, o config.Options, so, se *bytes.Buffer) int {
		return restoreCmd(c, a, o, so, se)
	}, args...)
}

func TestBackupRestoreCLI(t *testing.T) {
	src := makeDataDir(t)
	work := t.TempDir()
	pass := filepath.Join(work, "pass")
	if err := os.WriteFile(pass, []byte("hunter2 hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(work, "bad")
	if err := os.WriteFile(bad, []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bbk := filepath.Join(work, "x.bbk")

	if code, _ := backupRun(t, "--data-dir", src); code != exitUsage {
		t.Errorf("backup without --out = %d, want %d", code, exitUsage)
	}
	stdinIsTerminal = func() bool { return false }
	if code, msg := backupRun(t, "--data-dir", src, "--out", bbk); code != exitUsage || !strings.Contains(msg, "--passphrase-file") {
		t.Errorf("backup without passphrase on non-TTY = %d %q", code, msg)
	}
	if code, msg := backupRun(t, "--data-dir", src, "--out", bbk, "--passphrase-file", pass); code != exitOK {
		t.Fatalf("backup = %d %q", code, msg)
	}
	dst := filepath.Join(work, "dst")
	if code, msg := restoreRun(t, bbk, "--data-dir", dst, "--passphrase-file", bad); code != exitRuntime || !strings.Contains(msg, "wrong passphrase") {
		t.Errorf("wrong passphrase = %d %q", code, msg)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("wrong passphrase created the target")
	}
	if code, msg := restoreRun(t, bbk, "--data-dir", dst); code != exitUsage || !strings.Contains(msg, "--passphrase-file") {
		t.Errorf("restore without passphrase = %d %q", code, msg)
	}
	if code, msg := restoreRun(t, bbk, "--data-dir", dst, "--passphrase-file", pass); code != exitOK {
		t.Fatalf("restore = %d %q", code, msg)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "tls", "ca.crt")); string(b) != "CERT" {
		t.Errorf("ca.crt = %q", b)
	}
	if code, _ := restoreRun(t, bbk, "--data-dir", dst, "--passphrase-file", pass); code != exitRuntime {
		t.Errorf("restore into non-empty dir = %d, want %d", code, exitRuntime)
	}
	lock, err := config.AcquireLock(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if code, _ := restoreRun(t, bbk, "--data-dir", dst, "--passphrase-file", pass, "--force"); code != exitDirInUse {
		t.Errorf("restore while locked = %d, want %d", code, exitDirInUse)
	}
}

func TestBackupNoEncryptWarns(t *testing.T) {
	src := makeDataDir(t)
	bbk := filepath.Join(t.TempDir(), "plain.bbk")
	code, msg := backupRun(t, "--data-dir", src, "--out", bbk, "--no-encrypt")
	if code != exitOK || !strings.Contains(msg, "WARNING") || !strings.Contains(msg, "CA private key") {
		t.Fatalf("backup --no-encrypt = %d %q", code, msg)
	}
}

func TestHealthcheckWithoutCAFails(t *testing.T) {
	var out, errb bytes.Buffer
	code := healthcheckCmd(context.Background(), []string{"--data-dir", t.TempDir()}, config.Options{Getenv: func(string) string { return "" }}, &out, &errb)
	if code != exitRuntime {
		t.Fatalf("healthcheck = %d %q, want %d", code, errb.String(), exitRuntime)
	}
}
