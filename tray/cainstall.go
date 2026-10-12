package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/donaldsteele/bubbles/tray/internal/core"
)

// caHelperTimeout bounds the helper: the OS may show a confirmation dialog
// the user has to answer.
const caHelperTimeout = 3 * time.Minute

// installCA adds the confirmed CA to the current user's trust store. The
// command is built by core.CAInstallFor and executed directly, never through
// a shell. On Linux there is no command: the instructions are returned for
// the user to follow. The returned text is shown in the sign-in window.
func installCA(ctx context.Context, dataDir string, der []byte, confirmed string) (string, error) {
	home, _ := os.UserHomeDir()
	path, err := core.WriteCAPEM(dataDir, der, confirmed)
	if err != nil {
		return "", err
	}
	plan, err := core.CAInstallFor(runtime.GOOS, path, home)
	if err != nil {
		return "", err
	}
	if plan.Command == nil {
		return plan.Instructions, nil
	}
	ctx, cancel := context.WithTimeout(ctx, caHelperTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, plan.Command.Name, plan.Command.Args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg == "" {
			return "", fmt.Errorf("the trust helper failed: %w", err)
		}
		return "", errors.New("the trust helper failed: " + msg)
	}
	return "The certificate was added to your trusted roots.", nil
}
