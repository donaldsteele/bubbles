//go:build e2e

package app

import (
	"os"
	"strings"
)

// bootstrapPassword lets the end-to-end suite fix the first admin's password
// with BUBBLES_TEST_ADMIN_PASSWORD (or Options.AdminPassword). It applies to
// the first bootstrap only: the password a factory reset generates later
// must stay the one the reset showed. It only exists in binaries built with
// -tags e2e.
func (a *App) bootstrapPassword() string {
	pw := strings.TrimSpace(a.opts.AdminPassword)
	if pw == "" {
		pw = strings.TrimSpace(os.Getenv("BUBBLES_TEST_ADMIN_PASSWORD"))
	}
	a.opts.AdminPassword = ""
	// The variable stays set in the process; clear it so a rebuild after a
	// factory reset generates a password.
	os.Unsetenv("BUBBLES_TEST_ADMIN_PASSWORD")
	return pw
}
