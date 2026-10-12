//go:build windows

package service

import "context"

// addFirewallRules replaces any previous bubbles rules with fresh ones.
func addFirewallRules(ctx context.Context, r Runner, httpsPort, httpPort int) error {
	removeFirewallRules(ctx, r)
	return runAll(ctx, r, FirewallAddCommands(httpsPort, httpPort))
}

// removeFirewallRules is best effort: a missing rule is not an error.
func removeFirewallRules(ctx context.Context, r Runner) {
	for _, c := range FirewallDeleteCommands() {
		_, _ = r.Run(ctx, c[0], c[1:]...)
	}
}
