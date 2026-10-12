//go:build windows

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// platformDataDir returns the default data directory for this OS.
func platformDataDir(asService bool, getenv func(string) string) (string, error) {
	return windowsDataDir(asService, getenv)
}

// lockFile takes a non-blocking exclusive lock on the first byte of f. A
// conflicting lock, even from another handle in this process, yields ErrLocked.
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
