//go:build !windows

package config

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

// platformDataDir returns the default data directory for this OS.
func platformDataDir(asService bool, getenv func(string) string) (string, error) {
	home := getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if runtime.GOOS == "darwin" {
		return darwinDataDir(asService, home)
	}
	return linuxDataDir(asService, getenv, home)
}

// lockFile takes a non-blocking exclusive flock on f. A conflicting lock,
// even from another open file in this process, yields ErrLocked.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return ErrLocked
		default:
			return err
		}
	}
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
