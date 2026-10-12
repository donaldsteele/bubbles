package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName is the single-instance lock inside the data directory.
const LockFileName = "bubbles.lock"

// ErrLocked means another process already holds the data directory. The CLI
// maps it to exit code 3. Test with errors.Is.
var ErrLocked = errors.New("data directory is locked by a running bubbles server")

// Lock holds the exclusive single-instance lock on a data directory. The
// operating system drops it if the process dies, so a crash never leaves a
// stale lock behind.
type Lock struct {
	f *os.File
}

// AcquireLock takes the exclusive lock on <dataDir>/bubbles.lock, creating
// the file (mode 0600) if needed. It does not block: if another holder exists
// it returns an error wrapping ErrLocked. The data directory must exist.
func AcquireLock(dataDir string) (*Lock, error) {
	path := filepath.Join(dataDir, LockFileName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, FileMode)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// Release drops the lock. The lock file itself is left in place. Calling it
// on a nil or already released Lock is a no-op.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	uerr := unlockFile(f)
	if cerr := f.Close(); uerr == nil {
		uerr = cerr
	}
	return uerr
}
