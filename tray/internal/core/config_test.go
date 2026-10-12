package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestConfigRoundTrip(t *testing.T) {
	s := ConfigStore{Path: filepath.Join(t.TempDir(), "sub", "config.json")}
	got, err := s.Load()
	if err != nil || got != (Config{}) {
		t.Fatalf("missing file = %+v, %v", got, err)
	}
	want := Config{
		ServerURL: "https://192.168.1.20:28225", OnboardingURL: "http://192.168.1.20:28226",
		CAFingerprint: "sha256:ab", DeviceID: "d1", Username: "alice", DeviceName: "Laptop", PausedUntil: 42,
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || got != want {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(s.Path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", st.Mode(), err)
		}
	}
	if err := os.WriteFile(s.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("corrupt config accepted")
	}
}

func TestConfigSignedIn(t *testing.T) {
	c := Config{ServerURL: "https://h:28225", DeviceID: "d", CAFingerprint: "sha256:x"}
	if !c.SignedIn() {
		t.Fatal("complete config is not signed in")
	}
	for _, mod := range []func(*Config){
		func(c *Config) { c.ServerURL = "" },
		func(c *Config) { c.DeviceID = "" },
		func(c *Config) { c.CAFingerprint = "" },
	} {
		c := c
		mod(&c)
		if c.SignedIn() {
			t.Errorf("%+v signed in", c)
		}
	}
}

func TestNormalizeServerURL(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{"192.168.1.20", "https://192.168.1.20:28225", false},
		{"bubbles.local", "https://bubbles.local:28225", false},
		{"bubbles.local:9000", "https://bubbles.local:9000", false},
		{" https://host:28225/ ", "https://host:28225", false},
		{"https://[fe80::1]:28225", "https://[fe80::1]:28225", false},
		{"[::1]", "https://[::1]:28225", false},
		{"", "", true},
		{"http://host:28225", "", true},
		{"https://host:28225/app", "", true},
		{"https://host:28225/?x=1", "", true},
		{"https://user:pw@host", "", true},
		{"https://", "", true},
		{"ftp://host", "", true},
	}
	for _, tc := range tests {
		got, err := NormalizeServerURL(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("NormalizeServerURL(%q) = %q, %v; want %q bad=%v", tc.in, got, err, tc.want, tc.bad)
		}
	}
}

func TestOnboardingURLFor(t *testing.T) {
	tests := []struct {
		server, port, want string
		bad                bool
	}{
		{"https://192.168.1.20:28225", "", "http://192.168.1.20:28226", false},
		{"https://bubbles.local:28225", "9090", "http://bubbles.local:9090", false},
		{"https://[fe80::1]:28225", "", "http://[fe80::1]:28226", false},
		{"https://h:28225", "80;rm", "", true},
		{"::bad", "", "", true},
	}
	for _, tc := range tests {
		got, err := OnboardingURLFor(tc.server, tc.port)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("OnboardingURLFor(%q, %q) = %q, %v", tc.server, tc.port, got, err)
		}
	}
}

func TestPauseUntil(t *testing.T) {
	now := time.Date(2026, 3, 1, 21, 30, 0, 0, time.UTC)
	h, err := PauseUntil("1h", now)
	if err != nil || !h.Equal(now.Add(time.Hour)) {
		t.Fatalf("1h = %v, %v", h, err)
	}
	tm, err := PauseUntil("tomorrow", now)
	if err != nil || !tm.Equal(time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("tomorrow = %v, %v", tm, err)
	}
	if _, err := PauseUntil("forever", now); err == nil {
		t.Fatal("unknown pause accepted")
	}
}

func TestMemoryTokens(t *testing.T) {
	var s MemoryTokens
	if _, err := s.Get("a"); err != ErrNoToken {
		t.Fatalf("empty store: %v", err)
	}
	_ = s.Set("a", "bub_x")
	if tok, err := s.Get("a"); err != nil || tok != "bub_x" {
		t.Fatalf("Get = %q, %v", tok, err)
	}
	_ = s.Delete("a")
	if _, err := s.Get("a"); err != ErrNoToken {
		t.Fatalf("after delete: %v", err)
	}
}
