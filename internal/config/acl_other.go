//go:build !windows

package config

import (
	"errors"
	"os"
)

// restrictDir limits path to its owner (mode 0700).
func restrictDir(path string) error {
	return os.Chmod(path, DirMode)
}

// restrictRoot is restrictDir for the data directory itself. The root may be a
// pre-existing mount owned by someone else (a Docker volume), where tightening
// it is not ours to do, so a permission error is tolerated.
func restrictRoot(path string) error {
	if err := restrictDir(path); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}
