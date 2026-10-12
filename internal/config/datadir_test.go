package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultDataDirs(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return fakeEnv(kv) }

	t.Run("windows user", func(t *testing.T) {
		got, err := windowsDataDir(false, env(map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`, "ProgramData": `C:\PD`}))
		want := filepath.Join(`C:\Users\u\AppData\Local`, "bubbles")
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})
	t.Run("windows user without LOCALAPPDATA", func(t *testing.T) {
		got, err := windowsDataDir(false, env(map[string]string{"USERPROFILE": `C:\Users\u`}))
		want := filepath.Join(`C:\Users\u`, "AppData", "Local", "bubbles")
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})
	t.Run("windows nothing set", func(t *testing.T) {
		if _, err := windowsDataDir(false, env(nil)); err == nil {
			t.Error("expected an error")
		}
	})
	t.Run("windows service", func(t *testing.T) {
		got, err := windowsDataDir(true, env(map[string]string{"ProgramData": `D:\PD`, "LOCALAPPDATA": `C:\LA`}))
		if want := filepath.Join(`D:\PD`, "bubbles"); err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
		got, err = windowsDataDir(true, env(nil))
		if want := filepath.Join(`C:\ProgramData`, "bubbles"); err != nil || got != want {
			t.Errorf("fallback got %q, %v; want %q", got, err, want)
		}
	})
	t.Run("macOS", func(t *testing.T) {
		got, err := darwinDataDir(false, "/Users/u")
		if want := filepath.Join("/Users/u", "Library", "Application Support", "bubbles"); err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
		if got, _ := darwinDataDir(true, "/Users/u"); got != "/Library/Application Support/bubbles" {
			t.Errorf("daemon dir = %q", got)
		}
		if _, err := darwinDataDir(false, ""); err == nil {
			t.Error("expected an error without a home directory")
		}
	})
	t.Run("linux", func(t *testing.T) {
		xdg := filepath.Join(t.TempDir(), "xdg") // absolute on every OS
		got, err := linuxDataDir(false, env(map[string]string{"XDG_DATA_HOME": xdg}), "/home/u")
		if want := filepath.Join(xdg, "bubbles"); err != nil || got != want {
			t.Errorf("XDG: got %q, %v; want %q", got, err, want)
		}
		got, err = linuxDataDir(false, env(nil), "/home/u")
		if want := filepath.Join("/home/u", ".local", "share", "bubbles"); err != nil || got != want {
			t.Errorf("home: got %q, %v; want %q", got, err, want)
		}
		got, _ = linuxDataDir(false, env(map[string]string{"XDG_DATA_HOME": "relative/dir"}), "/home/u")
		if want := filepath.Join("/home/u", ".local", "share", "bubbles"); got != want {
			t.Errorf("relative XDG_DATA_HOME must be ignored: got %q, want %q", got, want)
		}
		if got, _ := linuxDataDir(true, env(nil), ""); got != "/var/lib/bubbles" {
			t.Errorf("service dir = %q", got)
		}
		if _, err := linuxDataDir(false, env(nil), ""); err == nil {
			t.Error("expected an error without a home directory")
		}
	})
}

func TestPlatformDataDirMatchesOS(t *testing.T) {
	env := fakeEnv(map[string]string{
		"LOCALAPPDATA": `C:\LA`, "ProgramData": `C:\PD`, "HOME": "/home/u", "USERPROFILE": `C:\U`,
	})
	got, err := platformDataDir(false, env)
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "windows":
		if want := filepath.Join(`C:\LA`, "bubbles"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	case "darwin":
		if want := filepath.Join("/home/u", "Library", "Application Support", "bubbles"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	default:
		if !strings.HasSuffix(got, filepath.Join(".local", "share", "bubbles")) {
			t.Errorf("got %q", got)
		}
	}
}

func TestEnsureDataDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "bubbles")
	for i := 0; i < 2; i++ { // second call proves idempotence
		if err := EnsureDataDir(dir); err != nil {
			t.Fatalf("EnsureDataDir (call %d): %v", i+1, err)
		}
	}
	for _, sub := range []string{DirFiles, DirTmp, DirTLS, DirLogs, DirBackups} {
		info, err := os.Stat(filepath.Join(dir, sub))
		if err != nil || !info.IsDir() {
			t.Errorf("%s directory missing: %v", sub, err)
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %o, want 700", sub, info.Mode().Perm())
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("data dir mode = %o, want 700", info.Mode().Perm())
		}
	}
}

func TestEnsureDataDirErrors(t *testing.T) {
	if err := EnsureDataDir(""); err == nil {
		t.Error("empty path accepted")
	}
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDataDir(filepath.Join(file, "sub")); err == nil {
		t.Error("path below a regular file accepted")
	}
}
