package tlsca

import (
	"crypto/x509"
	"net/netip"
	"slices"
	"strings"
)

// SANs is the subject alternative name set of a leaf certificate.
type SANs struct {
	DNS []string
	IPs []netip.Addr
}

// SANInput is what the SAN set is derived from.
type SANInput struct {
	// Addrs are the ranked private LAN addresses (netutil.Addrs).
	Addrs []netip.Addr
	// Hostname is the OS host name; invalid names are skipped.
	Hostname string
	// MDNSName is the single-label mDNS name, advertised as <name>.local.
	MDNSName string
	// Extra are the --tls-extra-dns names.
	Extra []string
}

var loopbackSANs = []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")}

// Discover returns the SAN set for in: the LAN addresses in rank order,
// 127.0.0.1, ::1, localhost, <hostname>, <short hostname>.local,
// <mdns-name>.local and the extra names, de-duplicated.
func Discover(in SANInput) SANs {
	var s SANs
	seenIP := map[netip.Addr]bool{}
	addIP := func(a netip.Addr) {
		a = a.Unmap().WithZone("")
		if a.IsValid() && !a.IsUnspecified() && !seenIP[a] {
			seenIP[a] = true
			s.IPs = append(s.IPs, a)
		}
	}
	for _, a := range in.Addrs {
		addIP(a)
	}
	for _, a := range loopbackSANs {
		addIP(a)
	}

	seenDNS := map[string]bool{}
	addDNS := func(d string) {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if d != "" && !seenDNS[d] && ValidDNSName(d) == nil {
			seenDNS[d] = true
			s.DNS = append(s.DNS, d)
		}
	}
	addDNS("localhost")
	if h := NormalizeHostname(in.Hostname); h != "" {
		addDNS(h)
		short, _, _ := strings.Cut(h, ".")
		addDNS(short + ".local")
	}
	if in.MDNSName != "" {
		addDNS(in.MDNSName + ".local")
	}
	for _, d := range in.Extra {
		addDNS(d)
	}
	return s
}

// SANsOf returns the SAN set of a certificate.
func SANsOf(cert *x509.Certificate) SANs {
	s := SANs{DNS: slices.Clone(cert.DNSNames)}
	for _, ip := range cert.IPAddresses {
		if a, ok := netip.AddrFromSlice(ip); ok {
			s.IPs = append(s.IPs, a.Unmap())
		}
	}
	return s
}

// Strings returns the IP addresses followed by the DNS names, as text.
func (s SANs) Strings() []string {
	out := make([]string, 0, len(s.IPs)+len(s.DNS))
	for _, ip := range s.IPs {
		out = append(out, ip.String())
	}
	return append(out, s.DNS...)
}

// Equal reports whether s and o contain the same names, ignoring order.
func (s SANs) Equal(o SANs) bool {
	a, b := s.Strings(), o.Strings()
	if len(a) != len(b) {
		return false
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
