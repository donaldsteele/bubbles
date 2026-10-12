package push

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"github.com/donaldsteele/bubbles/internal/netutil"
)

// ErrBlockedAddress is returned when a push destination resolves to an
// address that is not a public unicast address.
var ErrBlockedAddress = errors.New("push: destination address is not allowed")

// extraBlocked are ranges netutil.IsPrivate does not cover that must never be
// a push destination: "this network", IETF protocol assignments, documentation
// and benchmarking nets, reserved space, and the IPv6 transition ranges that
// embed an IPv4 address (NAT64, 6to4, Teredo) or discard traffic.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),         // deprecated IPv4-compatible addresses
	netip.MustParsePrefix("::ffff:0:0/96"), // IPv4-mapped (see BlockedAddr)
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// BlockedAddr reports whether a is not a public unicast address: private,
// loopback, link-local (including the 169.254.169.254 metadata address),
// CGNAT, multicast, unspecified, broadcast, reserved or documentation space.
// IPv4-mapped IPv6 addresses are judged by their IPv4 form and, because a
// resolver has no business returning them, refused outright as well.
func BlockedAddr(a netip.Addr) bool {
	a = a.WithZone("")
	if a.Is4In6() {
		return true
	}
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || netutil.IsPrivate(a) {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolver looks up host names. *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// guardedDialer resolves the destination itself, refuses it when any answer
// is not a public address, and dials the validated IP literal, so a DNS answer
// cannot change between the check and the connection.
type guardedDialer struct {
	resolver Resolver
	// dial opens the connection; nil dials with a net.Dialer that re-checks
	// the final address.
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func newGuardedDialer(r Resolver) *guardedDialer {
	if r == nil {
		r = net.DefaultResolver
	}
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		// Last line of defence: whatever address the OS is about to connect
		// to must be public.
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedAddress
			}
			if ip, err := netip.ParseAddr(host); err != nil || BlockedAddr(ip) {
				return ErrBlockedAddress
			}
			return nil
		},
	}
	return &guardedDialer{resolver: r, dial: d.DialContext}
}

// DialContext implements http.Transport.DialContext.
func (g *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("push: bad destination address: %w", err)
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else if addrs, err = g.resolver.LookupNetIP(ctx, "ip", host); err != nil {
		return nil, fmt.Errorf("push: resolve destination: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("push: destination did not resolve")
	}
	for _, a := range addrs {
		if BlockedAddr(a) {
			return nil, ErrBlockedAddress
		}
	}
	var lastErr error
	for _, a := range addrs {
		conn, err := g.dial(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// newClient builds the HTTP client of the sender: no proxy (a proxy would
// bypass the address guard), no redirects (a push service answering 3xx is a
// failure, never a hop to another host), HTTP/2 allowed, TLS 1.2 or newer
// against the system roots, and a hard per-request timeout.
func newClient(dial func(ctx context.Context, network, address string) (net.Conn, error), tlsCfg *tls.Config, timeout time.Duration) *http.Client {
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
