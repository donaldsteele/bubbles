//go:build e2e

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/donaldsteele/bubbles/internal/auth"
)

// tokenHookDone makes the fixed token apply to the first start only; the token
// a factory reset issues later must stay the one the reset showed.
var tokenHookDone atomic.Bool

// applyTestHooks lets the end-to-end suite fix the first-run setup token with
// BUBBLES_TEST_SETUP_TOKEN (52 lowercase base32 characters). It only exists in
// binaries built with -tags e2e.
func (a *App) applyTestHooks(ctx context.Context) error {
	token := strings.TrimSpace(os.Getenv("BUBBLES_TEST_SETUP_TOKEN"))
	if token == "" || a.setupToken == "" || tokenHookDone.Swap(true) {
		return nil
	}
	path := filepath.Join(a.cfg.DataDir, auth.SetupTokenFile)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return fmt.Errorf("test hook: write setup token: %w", err)
	}
	got, err := a.auth.AdoptSetupToken(ctx)
	if err != nil {
		return fmt.Errorf("test hook: adopt setup token: %w", err)
	}
	if got != token {
		return fmt.Errorf("test hook: BUBBLES_TEST_SETUP_TOKEN is not a valid setup token")
	}
	a.setupToken = got
	return nil
}
