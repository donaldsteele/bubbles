// Package tailnet asks the local Tailscale client about this machine: whether
// it is on a tailnet, its MagicDNS name and addresses, and whether the
// tailnet can issue HTTPS certificates for it. It also fetches such a
// certificate (`tailscale cert`). It only runs the tailscale command line
// with fixed arguments; it never talks to the network itself, and a machine
// without Tailscale simply reports that it is not on a tailnet.
package tailnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/dnsname"
)

// Info is what Tailscale reports about this machine.
type Info struct {
	// Running is true when Tailscale is up and logged in to a tailnet.
	Running bool `json:"running"`
	// DNSName is this machine's MagicDNS name without the trailing dot,
	// for example "chat-pi.tail1234.ts.net"; "" when MagicDNS is off.
	DNSName string `json:"dns_name,omitempty"`
	// IPs are this machine's Tailscale addresses (100.x.y.z and fd7a:...).
	IPs []netip.Addr `json:"ips,omitempty"`
	// CertDomains are the names the tailnet can issue HTTPS certificates
	// for; empty when HTTPS certificates are not enabled in the tailnet.
	CertDomains []string `json:"cert_domains,omitempty"`
}

// CanCert reports whether the tailnet issues HTTPS certificates for
// DNSName.
func (i Info) CanCert() bool {
	if i.DNSName == "" {
		return false
	}
	for _, d := range i.CertDomains {
		if strings.EqualFold(d, i.DNSName) {
			return true
		}
	}
	return false
}

// ErrNoTailscale means the tailscale command line was not found.
var ErrNoTailscale = errors.New("tailscale is not installed (no tailscale command found)")

// Runner runs the tailscale command line with args and returns its
// standard output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Client asks Tailscale about this machine. The zero value finds the
// tailscale command line on first use.
type Client struct {
	// Run runs the tailscale command line; nil finds it (Find) and runs it.
	Run Runner
	// CacheFor is how long a Status answer is reused; 0 means a minute.
	CacheFor time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	cached  Info
	cacheAt time.Time
	cacheOK bool
}

// statusTimeout bounds `tailscale status`; certTimeout bounds `tailscale
// cert`, which may wait for a certificate authority.
const (
	statusTimeout = 5 * time.Second
	certTimeout   = 2 * time.Minute
)

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Run != nil {
		return c.Run(ctx, args...)
	}
	bin := Find()
	if bin == "" {
		return nil, ErrNoTailscale
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if msg != "" {
			return out, fmt.Errorf("tailscale %s: %w: %s", args[0], err, msg)
		}
		return out, fmt.Errorf("tailscale %s: %w", args[0], err)
	}
	return out, nil
}

// Status reports this machine's tailnet state. A machine without
// Tailscale, or with Tailscale stopped or logged out, reports
// Info{Running: false} and no error; errors are for a tailscale command
// line that answered something unexpected.
func (c *Client) Status(ctx context.Context) (Info, error) {
	c.mu.Lock()
	ttl := c.CacheFor
	if ttl <= 0 {
		ttl = time.Minute
	}
	if c.cacheOK && c.now().Sub(c.cacheAt) < ttl {
		info := c.cached
		c.mu.Unlock()
		return info, nil
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := c.run(ctx, "status", "--json")
	var info Info
	switch {
	case errors.Is(err, ErrNoTailscale):
		// Not installed: not on a tailnet.
	case err != nil && len(out) == 0:
		// Installed but not running (the daemon is stopped): not on a
		// tailnet either.
	default:
		info, err = ParseStatus(out)
		if err != nil {
			return Info{}, err
		}
	}
	c.mu.Lock()
	c.cached, c.cacheAt, c.cacheOK = info, c.now(), true
	c.mu.Unlock()
	return info, nil
}

// Forget drops the cached Status answer.
func (c *Client) Forget() {
	c.mu.Lock()
	c.cacheOK = false
	c.mu.Unlock()
}

// status is the part of `tailscale status --json` bubbles reads.
type status struct {
	BackendState string `json:"BackendState"`
	Self         *struct {
		DNSName      string   `json:"DNSName"`
		TailscaleIPs []string `json:"TailscaleIPs"`
	} `json:"Self"`
	CertDomains []string `json:"CertDomains"`
}

// ParseStatus reads the output of `tailscale status --json`.
func ParseStatus(b []byte) (Info, error) {
	var st status
	if err := json.Unmarshal(b, &st); err != nil {
		return Info{}, fmt.Errorf("tailscale status: %w", err)
	}
	if st.BackendState != "Running" || st.Self == nil {
		return Info{}, nil
	}
	info := Info{Running: true}
	if name := strings.ToLower(strings.TrimSuffix(st.Self.DNSName, ".")); dnsname.Valid(name) == nil && strings.Contains(name, ".") {
		info.DNSName = name
	}
	for _, s := range st.Self.TailscaleIPs {
		if a, err := netip.ParseAddr(s); err == nil {
			info.IPs = append(info.IPs, a.Unmap())
		}
	}
	for _, d := range st.CertDomains {
		d = strings.ToLower(strings.TrimSuffix(d, "."))
		if dnsname.Valid(d) == nil {
			info.CertDomains = append(info.CertDomains, d)
		}
	}
	return info, nil
}

// Cert fetches (or renews) the HTTPS certificate for domain from the
// tailnet into certFile and keyFile, using `tailscale cert`. Tailscale
// keeps its own copy and only asks for a new certificate when the old one
// is close to expiry, so calling it daily is cheap. The tailnet must have
// HTTPS certificates enabled, and the certificate is published in the
// public Certificate Transparency logs.
func (c *Client) Cert(ctx context.Context, domain, certFile, keyFile string) error {
	if dnsname.Valid(domain) != nil {
		return fmt.Errorf("tailscale cert: %q is not a DNS name", domain)
	}
	if err := os.MkdirAll(filepath.Dir(certFile), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, certTimeout)
	defer cancel()
	_, err := c.run(ctx, "cert", "--cert-file", certFile, "--key-file", keyFile, domain)
	return err
}

// Find returns the path of the tailscale command line, or "" when it is
// not installed: tailscale on PATH, else the standard install location of
// the Windows and macOS apps.
func Find() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
			if dir := os.Getenv(env); dir != "" {
				candidates = append(candidates, filepath.Join(dir, "Tailscale", "tailscale.exe"))
			}
		}
	case "darwin":
		candidates = append(candidates, "/Applications/Tailscale.app/Contents/MacOS/Tailscale")
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
