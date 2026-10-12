//go:build linux

package autostart

import (
	"errors"
	"os"
	"path/filepath"
)

func entryPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", DesktopFileName), nil
}

// Enabled reports whether the autostart entry exists.
func Enabled() (bool, error) {
	p, err := entryPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Enable writes the XDG autostart entry for exe.
func Enable(exe string) error {
	entry, err := DesktopEntry(exe)
	if err != nil {
		return err
	}
	p, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(entry), 0o644)
}

// Disable removes the autostart entry.
func Disable() error {
	p, err := entryPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
