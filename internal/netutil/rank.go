package netutil

import (
	"net/netip"
	"sort"
	"strings"
)

// Interface is a network interface reduced to the fields ranking needs, so
// Rank can be tested on fake data.
type Interface struct {
	Name     string
	Index    int
	Up       bool
	Loopback bool
	Addrs    []netip.Addr
}

// Candidate is a private LAN address on a physical-looking interface.
type Candidate struct {
	Interface string
	Index     int
	Addr      netip.Addr
}

// Addrs returns the addresses of cs in order.
func Addrs(cs []Candidate) []netip.Addr {
	out := make([]netip.Addr, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Addr)
	}
	return out
}

// virtualContains and virtualPrefixes name adapters that carry host-only or
// container traffic that LAN devices cannot reach. Matching is
// case-insensitive. Tailscale is deliberately not listed.
var (
	virtualContains = []string{"vethernet", "vmware", "virtualbox", "docker"}
	virtualPrefixes = []string{"br-", "veth", "utun", "awdl", "llw", "bridge"}
)

// IsVirtualName reports whether an interface name looks like a virtual,
// container or Apple-internal adapter that must not supply LAN addresses.
func IsVirtualName(name string) bool {
	n := strings.ToLower(name)
	for _, s := range virtualContains {
		if strings.Contains(n, s) {
			return true
		}
	}
	for _, s := range virtualPrefixes {
		if strings.HasPrefix(n, s) {
			return true
		}
	}
	return false
}

var (
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
	rfc1918a  = netip.MustParsePrefix("192.168.0.0/16")
	rfc1918b  = netip.MustParsePrefix("10.0.0.0/8")
	rfc1918c  = netip.MustParsePrefix("172.16.0.0/12")
	linkLocal = netip.MustParsePrefix("169.254.0.0/16")
	ula       = netip.MustParsePrefix("fc00::/7")
)

// tier orders address classes, best first: RFC 1918 ranges (192.168 first,
// as on most home routers), then CGNAT (Tailscale), IPv6 ULA, IPv4
// link-local (DHCP did not answer) and last IPv6 link-local.
func tier(a netip.Addr) int {
	switch {
	case rfc1918a.Contains(a):
		return 0
	case rfc1918b.Contains(a):
		return 1
	case rfc1918c.Contains(a):
		return 2
	case cgnat.Contains(a):
		return 3
	case ula.Contains(a):
		return 4
	case linkLocal.Contains(a):
		return 5
	default:
		return 6
	}
}

// Rank filters ifaces down to usable LAN addresses and orders them best
// first. Interfaces that are down, loopback or virtual are dropped, as are
// loopback, unspecified and multicast addresses and anything IsPrivate
// rejects. Ties break on interface index, then address; duplicate addresses
// keep their first (best) occurrence.
func Rank(ifaces []Interface) []Candidate {
	var out []Candidate
	for _, ifc := range ifaces {
		if !ifc.Up || ifc.Loopback || IsVirtualName(ifc.Name) {
			continue
		}
		for _, raw := range ifc.Addrs {
			a := normalize(raw)
			if !a.IsValid() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || !IsPrivate(a) {
				continue
			}
			out = append(out, Candidate{Interface: ifc.Name, Index: ifc.Index, Addr: a})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := tier(out[i].Addr), tier(out[j].Addr)
		if ti != tj {
			return ti < tj
		}
		if out[i].Index != out[j].Index {
			return out[i].Index < out[j].Index
		}
		return out[i].Addr.Less(out[j].Addr)
	})
	seen := make(map[netip.Addr]bool, len(out))
	deduped := out[:0]
	for _, c := range out {
		if seen[c.Addr] {
			continue
		}
		seen[c.Addr] = true
		deduped = append(deduped, c)
	}
	return deduped
}
