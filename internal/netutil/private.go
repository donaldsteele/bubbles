// Package netutil classifies network addresses, ranks the host's LAN
// interfaces and guards HTTP handlers so only LAN clients get through.
package netutil

import "net/netip"

// privatePrefixes are the ranges bubbles treats as "the LAN": RFC 1918, link
// local, CGNAT (Tailscale), loopback and the IPv6 ULA / link-local ranges.
var privatePrefixes = []netip.Prefix{
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

// IsPrivate reports whether addr is a private, link-local, CGNAT or loopback
// address. IPv4-mapped IPv6 addresses are judged by their IPv4 form and any
// IPv6 zone is ignored. The zero Addr is not private.
func IsPrivate(addr netip.Addr) bool {
	addr = normalize(addr)
	if !addr.IsValid() {
		return false
	}
	for _, p := range privatePrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// normalize unmaps IPv4-in-IPv6 addresses and drops the zone so prefix
// containment checks behave.
func normalize(addr netip.Addr) netip.Addr {
	if !addr.IsValid() {
		return addr
	}
	return addr.Unmap().WithZone("")
}
