//go:build !e2e

package app

import "context"

// applyTestHooks is a no-op in production builds. The end-to-end suite builds
// the server with -tags e2e to get testhooks_e2e.go instead.
func (a *App) applyTestHooks(context.Context) error { return nil }
