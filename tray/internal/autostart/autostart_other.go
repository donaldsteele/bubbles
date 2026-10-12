//go:build !windows && !darwin && !linux

package autostart

// Enabled is always false: there is no login mechanism to inspect.
func Enabled() (bool, error) { return false, ErrUnsupported }

// Enable is unsupported.
func Enable(string) error { return ErrUnsupported }

// Disable is unsupported.
func Disable() error { return ErrUnsupported }
