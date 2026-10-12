package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Subdirectories of the data directory that EnsureDataDir creates.
const (
	DirFiles   = "files"
	DirTmp     = "tmp"
	DirTLS     = "tls"
	DirLogs    = "logs"
	DirBackups = "backups"
)

// Permissions for data directory contents on Unix. On Windows the protected
// DACL set by EnsureDataDir plays this role.
const (
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600
)

var dataSubdirs = []string{DirFiles, DirTmp, DirTLS, DirLogs, DirBackups}

// EnsureDataDir creates dir and its subdirectories (files, tmp, tls, logs,
// backups) readable by the owner only: mode 0700 on Unix, a protected DACL
// granting SYSTEM, Administrators and the current user on Windows. It is safe
// to call repeatedly.
func EnsureDataDir(dir string) error {
	if dir == "" {
		return errors.New("data directory is empty")
	}
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := restrictRoot(dir); err != nil {
		return fmt.Errorf("restrict data directory: %w", err)
	}
	for _, sub := range dataSubdirs {
		p := filepath.Join(dir, sub)
		if err := os.MkdirAll(p, DirMode); err != nil {
			return fmt.Errorf("create %s directory: %w", sub, err)
		}
		if err := restrictDir(p); err != nil {
			return fmt.Errorf("restrict %s directory: %w", sub, err)
		}
	}
	return nil
}

// windowsDataDir is the default data directory on Windows: the per-user
// %LOCALAPPDATA%\bubbles, or %ProgramData%\bubbles for a service.
func windowsDataDir(asService bool, getenv func(string) string) (string, error) {
	if asService {
		base := getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "bubbles"), nil
	}
	if base := getenv("LOCALAPPDATA"); base != "" {
		return filepath.Join(base, "bubbles"), nil
	}
	if home := getenv("USERPROFILE"); home != "" {
		return filepath.Join(home, "AppData", "Local", "bubbles"), nil
	}
	return "", errors.New("cannot determine the default data directory: LOCALAPPDATA is not set; use --data-dir")
}

// darwinDataDir is the default on macOS: a user's Application Support folder,
// or the system-wide one for a launch daemon.
func darwinDataDir(asService bool, home string) (string, error) {
	if asService {
		return "/Library/Application Support/bubbles", nil
	}
	if home == "" {
		return "", errors.New("cannot determine the default data directory: no home directory; use --data-dir")
	}
	return filepath.Join(home, "Library", "Application Support", "bubbles"), nil
}

// linuxDataDir is the default on Linux: $XDG_DATA_HOME/bubbles (when absolute,
// per the XDG spec), else ~/.local/share/bubbles; /var/lib/bubbles for a
// service.
func linuxDataDir(asService bool, getenv func(string) string, home string) (string, error) {
	if asService {
		return "/var/lib/bubbles", nil
	}
	if x := getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "bubbles"), nil
	}
	if home == "" {
		return "", errors.New("cannot determine the default data directory: no home directory; use --data-dir")
	}
	return filepath.Join(home, ".local", "share", "bubbles"), nil
}
