package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRotationAtSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bubbles.log")
	w, err := NewRotatingWriter(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	line := func(i int) string { return fmt.Sprintf("line-%02d-%s\n", i, strings.Repeat("x", 20)) } // 30 bytes
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(line(i))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	read := func(p string) string {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return string(b)
	}
	// 3 lines (90 bytes) fit per file. Ten lines: live holds line 9, .1 holds
	// 6-8, .2 holds 3-5; lines 0-2 fell off the end.
	if got, want := read(path), line(9); got != want {
		t.Errorf("live file = %q, want %q", got, want)
	}
	if got, want := read(path+".1"), line(6)+line(7)+line(8); got != want {
		t.Errorf(".1 = %q, want %q", got, want)
	}
	if got, want := read(path+".2"), line(3)+line(4)+line(5); got != want {
		t.Errorf(".2 = %q, want %q", got, want)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Errorf("expected no .3 backup, stat err = %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("%d files in log dir, want 3", len(entries))
	}
}

func TestRotationSingleFileAndOversizeWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bubbles.log")
	w, err := NewRotatingWriter(path, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, s := range []string{"aaaa\n", "bbbb\n", "cccc-oversize-entry\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "cccc-oversize-entry\n" {
		t.Errorf("live file = %q, want only the newest entry", b)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("maxFiles=1 must not keep backups, stat err = %v", err)
	}
}

func TestRotatingWriterAppendsAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bubbles.log")
	for _, s := range []string{"one\n", "two\n"} {
		w, err := NewRotatingWriter(path, 1<<20, 2)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("late\n")); err == nil {
			t.Error("write after Close succeeded")
		}
	}
	if b, _ := os.ReadFile(path); string(b) != "one\ntwo\n" {
		t.Errorf("log = %q, want existing content kept and appended to", b)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("log file mode = %o, want 600", perm)
		}
	}
}

func TestNewRotatingWriterRejectsBadLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if _, err := NewRotatingWriter(path, 0, 3); err == nil {
		t.Error("size 0 accepted")
	}
	if _, err := NewRotatingWriter(path, 10, 0); err == nil {
		t.Error("0 files accepted")
	}
}

func TestNewWritesJSONFileAndTextConsole(t *testing.T) {
	dir := t.TempDir()
	var console bytes.Buffer
	logger, closer, err := New(Options{Dir: dir, Console: &console})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("listening", "addr", ":8443", "token", "bub_SUPERSECRET", SecretAttr("setup", "hunter2"))
	logger.Debug("hidden")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &rec); err != nil {
		t.Fatalf("log file is not one JSON line: %v (%q)", err, b)
	}
	if rec["msg"] != "listening" || rec["addr"] != ":8443" || rec["level"] != "INFO" {
		t.Errorf("unexpected record %v", rec)
	}
	if rec["token"] != Redacted || rec["setup"] != Redacted {
		t.Errorf("secrets not redacted: %v", rec)
	}
	for _, out := range []string{string(b), console.String()} {
		if strings.Contains(out, "SUPERSECRET") || strings.Contains(out, "hunter2") {
			t.Errorf("secret leaked: %q", out)
		}
		if strings.Contains(out, "hidden") {
			t.Errorf("debug line logged at info level: %q", out)
		}
	}
	if !strings.Contains(console.String(), "msg=listening") || !strings.Contains(console.String(), "addr=:8443") {
		t.Errorf("console is not slog text: %q", console.String())
	}
}

func TestNewRequiresDirAndHonoursLevel(t *testing.T) {
	if _, _, err := New(Options{}); err == nil {
		t.Error("New without Dir succeeded")
	}
	dir := t.TempDir()
	logger, closer, err := New(Options{Dir: dir, Level: slog.LevelDebug})
	if err != nil {
		t.Fatal(err)
	}
	logger.Debug("dbg")
	_ = closer.Close()
	if b, _ := os.ReadFile(filepath.Join(dir, FileName)); !strings.Contains(string(b), `"msg":"dbg"`) {
		t.Errorf("debug record missing: %q", b)
	}
}

func TestRedact(t *testing.T) {
	if got := Redact(""); got != "" {
		t.Errorf("Redact(\"\") = %q, want empty", got)
	}
	if got := Redact("bub_abcdef"); got != Redacted || strings.Contains(got, "abc") {
		t.Errorf("Redact = %q", got)
	}
}

func TestRedactAttrKeys(t *testing.T) {
	tests := []struct {
		name   string
		groups []string
		key    string
		redact bool
	}{
		{name: "access_token", key: "access_token", redact: true},
		{name: "api-key", key: "api-key", redact: true},
		{name: "password_hash", key: "password_hash", redact: true},
		{name: "body", key: "body", redact: true},
		{name: "filename", key: "filename", redact: true},
		{name: "upper case key", key: "Authorization", redact: true},
		{name: "set-cookie", key: "Set-Cookie", redact: true},
		{name: "endpoint substring", key: "push_endpoint", redact: true},
		{name: "group token", groups: []string{"token"}, key: "id", redact: true},
		{name: "nested under secret group", groups: []string{"http", "Secret"}, key: "status", redact: true},
		{name: "user_id", key: "user_id"},
		{name: "path", key: "path"},
		{name: "status", key: "status"},
		{name: "name stays plain", key: "name"},
		{name: "plain group", groups: []string{"http", "request"}, key: "status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := redactAttr(tc.groups, slog.String(tc.key, "v"))
			want := "v"
			if tc.redact {
				want = Redacted
			}
			if got.Key != tc.key || got.Value.String() != want {
				t.Errorf("redactAttr(%v, %q) = %q, want %q", tc.groups, tc.key, got.Value.String(), want)
			}
		})
	}
}

func TestRedactsGroupedAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: redactAttr}))
	logger.Info("req", slog.Group("token", "id", "abc"), slog.Group("http", "status", 200))
	logger.WithGroup("auth").Info("login", "password_hash", "h", "user_id", "u1")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d records, want 2: %q", len(lines), buf.String())
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatal(err)
	}
	if g, _ := first["token"].(map[string]any); g["id"] != Redacted {
		t.Errorf("group token not redacted: %v", first)
	}
	if g, _ := first["http"].(map[string]any); g["status"] != float64(200) {
		t.Errorf("plain group altered: %v", first)
	}
	if g, _ := second["auth"].(map[string]any); g["password_hash"] != Redacted || g["user_id"] != "u1" {
		t.Errorf("grouped attrs wrong: %v", second)
	}
}

func TestWriteRetriesReopenAfterFailedRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bubbles.log")
	w, err := NewRotatingWriter(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("aaaa\n")); err != nil {
		t.Fatal(err)
	}
	failReopen := true
	w.openFile = func(name string, flag int, perm os.FileMode) (*os.File, error) {
		if failReopen {
			return nil, os.ErrPermission
		}
		return os.OpenFile(name, flag, perm)
	}
	// 5 + 9 bytes exceed maxSize, so this write rotates and the reopen fails.
	if _, err := w.Write([]byte("bbbbbbbb\n")); err == nil {
		t.Fatal("write succeeded although the live file could not be reopened")
	}
	failReopen = false
	if _, err := w.Write([]byte("cccc\n")); err != nil {
		t.Fatalf("write after failed rotation: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "cccc\n" {
		t.Errorf("live file = %q, want %q", b, "cccc\n")
	}
	if b, _ := os.ReadFile(path + ".1"); string(b) != "aaaa\n" {
		t.Errorf(".1 = %q, want %q", b, "aaaa\n")
	}
}

// TestRotatingWriterReopenWipesLogs: Reopen closes the live file, lets clear
// delete every log file (Windows refuses while it is open) and starts afresh.
func TestRotatingWriterReopenWipesLogs(t *testing.T) {
	dir := t.TempDir()
	w, err := NewRotatingWriter(filepath.Join(dir, FileName), 1<<20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	fmt.Fprintln(w, "old line")
	if err := os.WriteFile(filepath.Join(dir, FileName+".1"), []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	clear := func() error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if err := w.Reopen(clear); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(w, "new line")
	entries, _ := os.ReadDir(dir)
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if len(entries) != 1 || string(b) != "new line\n" {
		t.Errorf("after Reopen: %d files, live file %q", len(entries), b)
	}
}
