package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecondLockFailsWithErrLocked(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("first AcquireLock: %v", err)
	}
	second, err := AcquireLock(dir)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock = %v, %v; want ErrLocked", second, err)
	}
	if second != nil {
		t.Error("second AcquireLock returned a non-nil Lock")
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}
	again, err := AcquireLock(dir)
	if err != nil {
		t.Fatalf("AcquireLock after Release: %v", err)
	}
	if err := again.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockFileCreatedPrivately(t *testing.T) {
	dir := t.TempDir()
	l, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	info, err := os.Stat(filepath.Join(dir, LockFileName))
	if err != nil {
		t.Fatalf("lock file missing: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestAcquireLockMissingDir(t *testing.T) {
	_, err := AcquireLock(filepath.Join(t.TempDir(), "missing"))
	if err == nil || errors.Is(err, ErrLocked) {
		t.Errorf("AcquireLock on a missing dir = %v, want a non-ErrLocked error", err)
	}
}

func TestNilLockRelease(t *testing.T) {
	var l *Lock
	if err := l.Release(); err != nil {
		t.Errorf("nil Release: %v", err)
	}
}
