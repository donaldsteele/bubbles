//go:build unix

package files

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// osFreeSpace returns the bytes available to unprivileged users on the volume
// that holds dir.
func osFreeSpace(dir string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// isDiskFull reports whether err means the volume has no space left or the
// account's disk quota is used up.
func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
