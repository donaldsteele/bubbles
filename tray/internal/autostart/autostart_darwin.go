//go:build darwin

package autostart

import (
	"errors"
	"os"
	"path/filepath"
)

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"), nil
}

// Enabled reports whether the LaunchAgent file exists.
func Enabled() (bool, error) {
	p, err := agentPath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Enable writes the LaunchAgent for exe; launchd loads it at the next login.
func Enable(exe string) error {
	plist, err := LaunchAgentPlist(exe)
	if err != nil {
		return err
	}
	p, err := agentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(plist), 0o644)
}

// Disable removes the LaunchAgent file.
func Disable() error {
	p, err := agentPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
