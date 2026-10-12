//go:build !windows

package service

import (
	"context"
	"os"
)

func isAdmin() bool { return os.Geteuid() == 0 }

// IsWindowsService reports whether the process was started by the Windows SCM.
func IsWindowsService() bool { return false }

// RunAsService is only meaningful on Windows.
func RunAsService(run func(ctx context.Context) int) error { return ErrUnsupported }
