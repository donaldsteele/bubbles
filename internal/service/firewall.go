package service

import "strconv"

// Firewall rule names. Only the private profile is opened: bubbles serves
// private networks only.
const (
	FirewallHTTPSRule      = "bubbles HTTPS"
	FirewallOnboardingRule = "bubbles onboarding"
)

// FirewallAddCommands returns the netsh commands that open the HTTPS and
// onboarding ports for inbound traffic on the private profile.
func FirewallAddCommands(httpsPort, httpPort int) [][]string {
	add := func(name string, port int) []string {
		return []string{"netsh", "advfirewall", "firewall", "add", "rule",
			"name=" + name, "dir=in", "action=allow", "protocol=TCP",
			"localport=" + strconv.Itoa(port), "profile=private"}
	}
	return [][]string{add(FirewallHTTPSRule, httpsPort), add(FirewallOnboardingRule, httpPort)}
}

// FirewallDeleteCommands removes the rules added by FirewallAddCommands.
func FirewallDeleteCommands() [][]string {
	del := func(name string) []string {
		return []string{"netsh", "advfirewall", "firewall", "delete", "rule", "name=" + name}
	}
	return [][]string{del(FirewallHTTPSRule), del(FirewallOnboardingRule)}
}
