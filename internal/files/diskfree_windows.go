//go:build windows

package files

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// osFreeSpace returns the bytes available to the server's account on the
// volume that holds dir.
func osFreeSpace(dir string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return 0, err
	}
	return avail, nil
}

// isDiskFull reports whether err means the volume has no space left.
func isDiskFull(err error) bool {
	return errors.Is(err, syscall.Errno(windows.ERROR_DISK_FULL)) ||
		errors.Is(err, syscall.Errno(windows.ERROR_HANDLE_DISK_FULL))
}
