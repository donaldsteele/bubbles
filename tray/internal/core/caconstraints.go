package core

import (
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ErrUnconstrainedCA is returned for a CA that is not limited to local names.
var ErrUnconstrainedCA = errors.New("this certificate is not limited to your local network, so it was not installed")

// caBaseDomains are always acceptable (and their subdomains). They mirror the
// base domains of the server CA in internal/tlsca/constraints.go.
var caBaseDomains = []string{"local", "home.arpa", "lan", "internal", "localdomain", "localhost"}

// caPrivatePrefixes mirror the private ranges in internal/tlsca/constraints.go.
var caPrivatePrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

var oidNameConstraints = asn1.ObjectIdentifier{2, 5, 29, 30}

// CheckCAConstraints refuses a CA unless it carries critical name constraints
// that limit it to LAN names and private IP ranges, so even a rogue CA could
// not intercept public HTTPS sites.
func CheckCAConstraints(cert *x509.Certificate) error {
	critical := cert.PermittedDNSDomainsCritical
	for _, e := range cert.Extensions {
		if e.Id.Equal(oidNameConstraints) && e.Critical {
			critical = true
		}
	}
	if !critical {
		return fmt.Errorf("%w (no critical name constraints)", ErrUnconstrainedCA)
	}
	if len(cert.PermittedDNSDomains) == 0 {
		return fmt.Errorf("%w (no permitted DNS names)", ErrUnconstrainedCA)
	}
	for _, d := range cert.PermittedDNSDomains {
		if !caDomainOK(d) {
			return fmt.Errorf("%w (DNS name %q is too broad)", ErrUnconstrainedCA, d)
		}
	}
	if len(cert.PermittedIPRanges) == 0 {
		return fmt.Errorf("%w (no permitted IP ranges)", ErrUnconstrainedCA)
	}
	for _, n := range cert.PermittedIPRanges {
		if !caRangeOK(n) {
			return fmt.Errorf("%w (IP range %s is not private)", ErrUnconstrainedCA, n)
		}
	}
	return nil
}

func caDomainOK(d string) bool {
	d = strings.ToLower(strings.TrimPrefix(d, "."))
	if d == "" {
		return false
	}
	for _, b := range caBaseDomains {
		if d == b || strings.HasSuffix(d, "."+b) {
			return true
		}
	}
	ps, icann := publicsuffix.PublicSuffix(d)
	return !(ps == d && (icann || strings.Contains(d, ".")))
}

func caRangeOK(n *net.IPNet) bool {
	if n == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(n.IP)
	ones, bits := n.Mask.Size()
	if !ok || bits == 0 {
		return false
	}
	if addr.Is4In6() && bits == 32 {
		addr = addr.Unmap()
	}
	if addr.BitLen() != bits {
		return false
	}
	p, err := addr.Prefix(ones)
	if err != nil {
		return false
	}
	for _, priv := range caPrivatePrefixes {
		if priv.Addr().BitLen() == p.Addr().BitLen() && priv.Contains(p.Addr()) && p.Bits() >= priv.Bits() {
			return true
		}
	}
	return false
}
