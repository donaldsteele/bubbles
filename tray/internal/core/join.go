package core

import (
	"net/url"
	"strings"
)

// Tab names of the account screen.
const (
	TabCreate = "create"
	TabSignIn = "signin"
)

// ServerAddress is the host:port of a server URL, for display. It falls back
// to the text itself when it does not parse as a URL with a host.
func ServerAddress(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	return u.Host
}

// JoinLabel is the text of a discovered server's button: "Join {name} · {address}",
// or "Join {address}" when the server has no name.
func JoinLabel(name, serverURL string) string {
	addr := ServerAddress(serverURL)
	name = strings.TrimSpace(name)
	if name == "" {
		return "Join " + addr
	}
	return "Join " + name + " · " + addr
}

// DefaultTab picks the tab the account screen opens on: creating an account
// when sign-up is open and this computer has no saved name for the server,
// signing in otherwise.
func DefaultTab(registrationOpen bool, savedUsername string) string {
	if registrationOpen && strings.TrimSpace(savedUsername) == "" {
		return TabCreate
	}
	return TabSignIn
}
