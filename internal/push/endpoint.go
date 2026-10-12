// Package push is bubbles' Web Push: VAPID keys, the subscription store, the
// payload builder and the sender. Web Push is the only traffic the server
// originates, and the destination URL is supplied by users, so every
// destination passes two independent checks: the endpoint allowlist at
// subscription time (ValidateEndpoint) and the address guard at connection
// time (guardedDialer).
package push

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// MaxEndpointLen bounds a subscription endpoint.
const MaxEndpointLen = 2048

// exactHosts and hostSuffixes are the browser push services a subscription
// may point at: Firebase Cloud Messaging (Chrome, Edge, Opera, Samsung),
// Mozilla autopush (Firefox), Apple web push (Safari) and WNS (legacy Edge).
// A suffix matches any host that ends in it with at least one label before.
var (
	exactHosts = map[string]bool{
		"fcm.googleapis.com":                true,
		"updates.push.services.mozilla.com": true,
		"web.push.apple.com":                true,
	}
	hostSuffixes = []string{
		".push.services.mozilla.com",
		".notify.windows.com",
		".push.apple.com",
	}
)

// ValidateEndpoint checks that raw is a URL the server may POST to: https, no
// credentials, no fragment, port 443 or none, and a host (never an IP
// literal) on the push service allowlist. The returned error text is safe to
// show to the user.
func ValidateEndpoint(raw string) error {
	if raw == "" {
		return errors.New("is required")
	}
	if len(raw) > MaxEndpointLen {
		return errors.New("is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "https" {
		return errors.New("must be an https URL")
	}
	if u.User != nil {
		return errors.New("must not contain credentials")
	}
	if u.Opaque != "" {
		return errors.New("is not a valid URL")
	}
	if u.Fragment != "" {
		return errors.New("must not contain a fragment")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("must use port 443")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("has no host")
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return errors.New("must name a push service, not an IP address")
	}
	if !AllowedHost(host) {
		return errors.New("is not a known browser push service")
	}
	return nil
}

// AllowedHost reports whether host (lower case, no port) is on the push
// service allowlist.
func AllowedHost(host string) bool {
	if exactHosts[host] {
		return true
	}
	for _, s := range hostSuffixes {
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return true
		}
	}
	return false
}
