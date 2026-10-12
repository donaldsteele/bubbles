package bubblesclient

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/brutella/dnssd"
)

// ServiceType is the mDNS service type bubbles servers advertise.
const ServiceType = "_bubbles._tcp"

// Server is a bubbles server found on the LAN.
type Server struct {
	// Name is the service instance name (the server's instance name).
	Name string
	// Host is the mDNS host name, for example "bubbles.local".
	Host string
	// Port is the HTTPS port.
	Port int
	// Addrs are the advertised addresses.
	Addrs []netip.Addr
	// FingerprintPrefix is the first 16 hex digits of the CA SHA-256 (TXT fp).
	FingerprintPrefix string
	// OnboardPort is the plain-HTTP onboarding port (TXT onboard), 0 if unknown.
	OnboardPort int
	// Version is the TXT record version.
	Version string
}

// URL is the HTTPS base URL, preferring the first IPv4 address.
func (s Server) URL() string {
	return "https://" + net.JoinHostPort(s.bestHost(), strconv.Itoa(s.Port))
}

// OnboardingURL is the plain-HTTP onboarding base URL, or "" if unknown.
func (s Server) OnboardingURL() string {
	if s.OnboardPort == 0 {
		return ""
	}
	return "http://" + net.JoinHostPort(s.bestHost(), strconv.Itoa(s.OnboardPort))
}

func (s Server) bestHost() string {
	for _, a := range s.Addrs {
		if a.Is4() {
			return a.String()
		}
	}
	for _, a := range s.Addrs {
		if !a.IsLinkLocalUnicast() {
			return a.String()
		}
	}
	return s.Host
}

// Discover browses for _bubbles._tcp for up to timeout and returns the
// servers found, ordered by name. Reaching the timeout is not an error.
func Discover(ctx context.Context, timeout time.Duration) ([]Server, error) {
	bctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		mu    sync.Mutex
		found = map[string]Server{}
	)
	err := dnssd.LookupType(bctx, ServiceType+".local.", func(e dnssd.BrowseEntry) {
		mu.Lock()
		defer mu.Unlock()
		s := found[e.Name]
		s.Name, s.Host, s.Port = e.Name, e.Host, e.Port
		for _, ip := range e.IPs {
			if a, ok := netip.AddrFromSlice(ip); ok {
				a = a.Unmap()
				if !containsAddr(s.Addrs, a) {
					s.Addrs = append(s.Addrs, a)
				}
			}
		}
		s.FingerprintPrefix = e.Text["fp"]
		s.Version = e.Text["v"]
		if n, err := strconv.Atoi(e.Text["onboard"]); err == nil {
			s.OnboardPort = n
		}
		found[e.Name] = s
	}, func(e dnssd.BrowseEntry) {
		mu.Lock()
		delete(found, e.Name)
		mu.Unlock()
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]Server, 0, len(found))
	for _, s := range found {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
