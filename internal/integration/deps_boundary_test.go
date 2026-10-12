package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDependencyBoundary enforces the public packages' import limits:
// pkg/protocol and pkg/notifyrules use the standard library only, and
// pkg/bubblesclient adds only coder/websocket and brutella/dnssd (plus their
// own transitive dependencies; on Linux dnssd also pulls in vishvananda's
// netlink and netns). Every server target OS is checked, whatever the host.
func TestDependencyBoundary(t *testing.T) {
	const module = "github.com/donaldsteele/bubbles"
	allowedThirdParty := map[string][]string{
		module + "/pkg/protocol":    nil,
		module + "/pkg/notifyrules": nil,
		module + "/pkg/bubblesclient": {
			"github.com/coder/websocket", "github.com/brutella/dnssd", "github.com/miekg/dns",
			"github.com/vishvananda/netlink", "github.com/vishvananda/netns",
			"golang.org/x/net", "golang.org/x/sys",
		},
	}
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for pkg, allow := range allowedThirdParty {
			t.Run(goos+"/"+pkg, func(t *testing.T) {
				cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", pkg)
				cmd.Env = append(os.Environ(), "GOOS="+goos)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("go list: %v", err)
				}
				for _, dep := range strings.Fields(string(out)) {
					if dep == pkg || (pkg == module+"/pkg/bubblesclient" && dep == module+"/pkg/protocol") {
						continue
					}
					if strings.HasPrefix(dep, module+"/") {
						t.Errorf("%s imports first-party package %s", pkg, dep)
						continue
					}
					ok := false
					for _, a := range allow {
						if dep == a || strings.HasPrefix(dep, a+"/") || (strings.HasSuffix(a, "/") && strings.HasPrefix(dep, a)) {
							ok = true
						}
					}
					if !ok {
						t.Errorf("%s depends on %s", pkg, dep)
					}
				}
			})
		}
	}
}
