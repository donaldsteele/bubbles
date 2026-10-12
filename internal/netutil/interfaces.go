package netutil

import (
	"fmt"
	"net"
	"net/netip"
)

// Candidates enumerates the host's network interfaces and returns the ranked
// private LAN addresses (see Rank).
func Candidates() ([]Candidate, error) {
	ifaces, err := Interfaces()
	if err != nil {
		return nil, err
	}
	return Rank(ifaces), nil
}

// Interfaces lists the host's network interfaces with their unicast
// addresses. An interface whose addresses cannot be read is reported with no
// addresses rather than failing the whole scan.
func Interfaces() ([]Interface, error) {
	sys, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	out := make([]Interface, 0, len(sys))
	for _, s := range sys {
		ifc := Interface{
			Name:     s.Name,
			Index:    s.Index,
			Up:       s.Flags&net.FlagUp != 0,
			Loopback: s.Flags&net.FlagLoopback != 0,
		}
		if addrs, err := s.Addrs(); err == nil {
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				if addr, ok := netip.AddrFromSlice(ipn.IP); ok {
					ifc.Addrs = append(ifc.Addrs, normalize(addr))
				}
			}
		}
		out = append(out, ifc)
	}
	return out, nil
}
