package core

import "testing"

func TestServerAddress(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://192.168.1.20:28225", "192.168.1.20:28225"},
		{"https://bubbles.local", "bubbles.local"},
		{" https://bubbles.local:1 ", "bubbles.local:1"},
		{"not a url", "not a url"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ServerAddress(tt.in); got != tt.want {
			t.Errorf("ServerAddress(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestJoinLabel(t *testing.T) {
	tests := []struct{ name, url, want string }{
		{"Home", "https://10.0.0.2:28225", "Join Home · 10.0.0.2:28225"},
		{"  ", "https://10.0.0.2:28225", "Join 10.0.0.2:28225"},
		{"", "https://bubbles.local", "Join bubbles.local"},
	}
	for _, tt := range tests {
		if got := JoinLabel(tt.name, tt.url); got != tt.want {
			t.Errorf("JoinLabel(%q, %q) = %q, want %q", tt.name, tt.url, got, tt.want)
		}
	}
}

func TestDefaultTab(t *testing.T) {
	tests := []struct {
		name  string
		open  bool
		saved string
		want  string
	}{
		{"open and new", true, "", TabCreate},
		{"open, blank saved name", true, "  ", TabCreate},
		{"open but known user", true, "ann", TabSignIn},
		{"closed and new", false, "", TabSignIn},
		{"closed and known", false, "ann", TabSignIn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultTab(tt.open, tt.saved); got != tt.want {
				t.Fatalf("DefaultTab = %q, want %q", got, tt.want)
			}
		})
	}
}
