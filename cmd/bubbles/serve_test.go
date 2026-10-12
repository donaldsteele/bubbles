package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/logging"
)

func runServeTest(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	opts := config.Options{Getenv: func(k string) string { return env[k] }}
	code = serve(context.Background(), args, opts, &out, &errb)
	return code, out.String(), errb.String()
}

func TestServePrintConfigDoesNotTouchDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	code, stdout, stderr := runServeTest(t, nil, "--print-config", "--data-dir", dir, "--mdns-name", "Chat")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	var got config.Config
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if got.DataDir != dir || got.MDNSName != "chat" || got.HTTPSAddr != ":8443" || got.HTTPAddr != ":8080" {
		t.Errorf("unexpected config %+v", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("--print-config created the data dir (stat err = %v)", err)
	}
}

func TestServePrintConfigFromEnv(t *testing.T) {
	dir := t.TempDir()
	code, stdout, _ := runServeTest(t, map[string]string{"BUBBLES_DATA_DIR": dir, "BUBBLES_PRINT_CONFIG": "1"})
	if code != exitOK || !strings.Contains(stdout, `"print_config": true`) {
		t.Errorf("exit = %d, stdout = %q", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, config.LockFileName)); !os.IsNotExist(err) {
		t.Errorf("print-config took the lock (stat err = %v)", err)
	}
}

func TestServeUsageErrors(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runServeTest(t, nil, "--data-dir", dir, "--tls-cert", "c.pem")
	if code != exitUsage || !strings.Contains(stderr, "--tls-cert and --tls-key") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if code, _, _ := runServeTest(t, nil, "--no-such-flag"); code != exitUsage {
		t.Errorf("unknown flag: exit = %d, want %d", code, exitUsage)
	}
	code, _, stderr = runServeTest(t, nil, "--help")
	if code != exitOK || !strings.Contains(stderr, "usage: bubbles serve") {
		t.Errorf("--help: exit = %d, stderr = %q", code, stderr)
	}
}

func TestServeExitsThreeWhenDataDirLocked(t *testing.T) {
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	code, _, stderr := runServeTest(t, nil, "--data-dir", dir, "--allow-public")
	if code != exitDirInUse {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitDirInUse, stderr)
	}
	if !strings.Contains(stderr, "already using") {
		t.Errorf("stderr = %q", stderr)
	}
}

// syncBuffer is a goroutine-safe bytes.Buffer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serveUntilBanner starts serve on free loopback ports, waits for the
// startup banner, then cancels and returns the exit code and stderr.
func serveUntilBanner(t *testing.T, dir string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errb syncBuffer
	done := make(chan int, 1)
	args := []string{"--data-dir", dir, "--allow-public", "--no-mdns",
		"--https-addr", "127.0.0.1:0", "--http-addr", "127.0.0.1:0"}
	go func() {
		done <- serve(ctx, args, config.Options{Getenv: func(string) string { return "" }}, io.Discard, &errb)
	}()

	deadline := time.After(30 * time.Second)
	for !strings.Contains(errb.String(), "CA fingerprint") {
		select {
		case code := <-done:
			t.Fatalf("serve exited early with %d: %s", code, errb.String())
		case <-deadline:
			t.Fatalf("no startup banner: %s", errb.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case code := <-done:
		return code, errb.String()
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after cancel")
		return 0, ""
	}
}

func TestServeRunsUntilCancelled(t *testing.T) {
	dir := t.TempDir()
	code, stderr := serveUntilBanner(t, dir)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	for _, want := range []string{"https://127.0.0.1:", "http://127.0.0.1:", "CA fingerprint"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	for _, p := range []string{
		filepath.Join(dir, config.DirLogs, logging.FileName),
		filepath.Join(dir, config.DirTLS, "ca.crt"),
		filepath.Join(dir, config.DirTLS, "leaf.crt"),
		filepath.Join(dir, "bubbles.db"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// The lock was released on return: a second run starts again.
	if code, stderr := serveUntilBanner(t, dir); code != exitOK {
		t.Errorf("second run exit = %d, stderr = %q", code, stderr)
	}
}

func TestServeRejectsBadExtraDNS(t *testing.T) {
	for _, v := range []string{"10.0.0.1", "bad_name.lan", "-x.lan", "a..lan"} {
		code, _, stderr := runServeTest(t, nil, "--data-dir", t.TempDir(), "--tls-extra-dns", v)
		if code != exitUsage || !strings.Contains(stderr, "tls-extra-dns") {
			t.Errorf("--tls-extra-dns %q: exit = %d, stderr = %q", v, code, stderr)
		}
	}
}
