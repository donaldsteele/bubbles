// Package core is the testable heart of the bubbles tray app: configuration,
// the CA trust state machine, the notification decision, the event monitor,
// CA-install command construction and icon rendering. It imports neither
// Wails nor any OS-specific package, so its tests need no CGO and no webview.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultOnboardingPort is the plain-HTTP onboarding port of a server.
const DefaultOnboardingPort = "8080"

// Config is what the tray keeps on disk (never a secret: the device token
// lives in the OS keychain).
type Config struct {
	// ServerURL is the HTTPS base URL of the signed-in server.
	ServerURL string `json:"server_url"`
	// OnboardingURL is the plain-HTTP onboarding base URL (CA download).
	OnboardingURL string `json:"onboarding_url"`
	// CAFingerprint is the pinned CA, "sha256:<64 hex>", set only after the
	// user confirmed it.
	CAFingerprint string `json:"ca_fingerprint"`
	// DeviceID is the server-side id of this device token, for sign-out.
	DeviceID string `json:"device_id"`
	// Username is shown in the menu.
	Username string `json:"username"`
	// DeviceName is what the server lists for this device.
	DeviceName string `json:"device_name"`
	// PausedUntil is the end of a notification pause, unix ms; 0 = none.
	PausedUntil int64 `json:"paused_until"`
}

// SignedIn reports whether the config describes a server we have a device on.
func (c Config) SignedIn() bool {
	return c.ServerURL != "" && c.DeviceID != "" && c.CAFingerprint != ""
}

// ConfigStore loads and saves Config as JSON in one file.
type ConfigStore struct {
	Path string
}

// Load reads the config; a missing file is the zero Config.
func (s ConfigStore) Load() (Config, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return c, nil
}

// Save writes the config atomically with owner-only permissions.
func (s ConfigStore) Save(c Config) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".config-*")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write config: %w", errors.Join(werr, cerr))
	}
	if err := os.Chmod(name, 0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = os.Remove(name)
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(name, s.Path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// NormalizeServerURL turns what a user typed ("192.168.1.20", "bubbles.local:8443",
// "https://host:8443/") into a clean https base URL. Plain http is refused:
// the tray only talks TLS to the server.
func NormalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter the server address")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", errors.New("that is not a valid server address")
	}
	if u.Scheme != "https" {
		return "", errors.New("the server address must use https")
	}
	if u.User != nil {
		return "", errors.New("the server address must not contain credentials")
	}
	if p := strings.Trim(u.Path, "/"); p != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("use the server root, without a path")
	}
	host := u.Host
	if u.Port() == "" {
		host = u.Hostname()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		host += ":8443"
	}
	return "https://" + host, nil
}

// OnboardingURLFor derives the onboarding base URL from the HTTPS server
// URL: same host, port DefaultOnboardingPort unless port is given.
func OnboardingURLFor(serverURL, port string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("invalid server address")
	}
	if port == "" {
		port = DefaultOnboardingPort
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return "", errors.New("the onboarding port must be a number")
		}
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port, nil
}
