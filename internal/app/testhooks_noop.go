//go:build !e2e

package app

// bootstrapPassword is Options.AdminPassword in production builds, used for
// the first bootstrap only: the password a factory reset generates later
// must stay the one the reset showed. The end-to-end suite builds the server
// with -tags e2e to get testhooks_e2e.go instead.
func (a *App) bootstrapPassword() string {
	pw := a.opts.AdminPassword
	a.opts.AdminPassword = ""
	return pw
}
