package tlsca

import (
	"crypto/x509"
	"net"
	"net/netip"
	"strings"

	"github.com/donaldsteele/bubbles/internal/dnsname"
)

// BaseDNSDomains are the DNS name constraints every bubbles CA carries,
// before the OS host name and --tls-extra-dns values are added.
var BaseDNSDomains = []string{"local", "home.arpa", "lan", "internal", "localdomain", "localhost"}

// PermittedIPRanges are the IP name constraints of every bubbles CA: the
// private, link-local, CGNAT and loopback ranges (see Locked Decision 2).
var PermittedIPRanges = []netip.Prefix{
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

// ValidDNSName reports whether name is a lower-case DNS name made of
// dot-separated labels of 1-63 characters [a-z0-9-] that neither start nor
// end with a hyphen, at most 253 characters in total, and not an IP literal.
func ValidDNSName(name string) error { return dnsname.Valid(name) }

// NormalizeHostname lower-cases an OS host name and drops a trailing dot. It
// returns "" when the result is not a valid DNS name (for example a Windows
// name containing an underscore), so callers can skip it.
func NormalizeHostname(h string) string {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if ValidDNSName(h) != nil {
		return ""
	}
	return h
}

// permittedDNS returns the DNS constraints for a new CA: the base domains,
// the host name and the extra names, de-duplicated in that order.
func permittedDNS(hostname string, extra []string) []string {
	out := make([]string, 0, len(BaseDNSDomains)+1+len(extra))
	seen := map[string]bool{}
	add := func(d string) {
		if d != "" && !seen[d] && ValidDNSName(d) == nil {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, d := range BaseDNSDomains {
		add(d)
	}
	add(NormalizeHostname(hostname))
	for _, d := range extra {
		add(strings.ToLower(strings.TrimSuffix(d, ".")))
	}
	return out
}

func permittedIPNets() []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(PermittedIPRanges))
	for _, p := range PermittedIPRanges {
		addr := p.Addr()
		nets = append(nets, &net.IPNet{
			IP:   net.IP(addr.AsSlice()),
			Mask: net.CIDRMask(p.Bits(), addr.BitLen()),
		})
	}
	return nets
}

// permitsDNS mirrors crypto/x509 constraint matching: a constraint permits
// the domain itself and every name below it.
func permitsDNS(cert *x509.Certificate, name string) bool {
	if len(cert.PermittedDNSDomains) == 0 {
		return true
	}
	for _, c := range cert.PermittedDNSDomains {
		c = strings.ToLower(strings.TrimPrefix(c, "."))
		if name == c || strings.HasSuffix(name, "."+c) {
			return true
		}
	}
	return false
}

func permitsIP(cert *x509.Certificate, addr netip.Addr) bool {
	if len(cert.PermittedIPRanges) == 0 {
		return true
	}
	ip := net.IP(addr.AsSlice())
	for _, n := range cert.PermittedIPRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Filter splits sans into the names the CA certificate's name constraints
// permit and the ones it would make invalid (for example after the host was
// renamed, or an extra DNS name was added after the CA was created).
func Filter(ca *x509.Certificate, sans SANs) (ok SANs, dropped []string) {
	for _, d := range sans.DNS {
		if permitsDNS(ca, d) {
			ok.DNS = append(ok.DNS, d)
		} else {
			dropped = append(dropped, d)
		}
	}
	for _, ip := range sans.IPs {
		if permitsIP(ca, ip) {
			ok.IPs = append(ok.IPs, ip)
		} else {
			dropped = append(dropped, ip.String())
		}
	}
	return ok, dropped
}
