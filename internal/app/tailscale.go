package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/tailnet"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// TailnetClient is the part of tailnet.Client the server uses; tests fake it.
type TailnetClient interface {
	Status(ctx context.Context) (tailnet.Info, error)
	Cert(ctx context.Context, domain, certFile, keyFile string) error
}

// noTailnet is a TailnetClient for a machine that is not on a tailnet.
type noTailnet struct{}

func (noTailnet) Status(context.Context) (tailnet.Info, error) { return tailnet.Info{}, nil }
func (noTailnet) Cert(context.Context, string, string, string) error {
	return errors.New("not on a tailnet")
}

// TailscaleCertInterval is how often the Tailscale certificate is renewed
// (tailscale cert only asks for a new one when the old one nears expiry).
const TailscaleCertInterval = 6 * time.Hour

// dirTailscale holds the Tailscale certificate files in the data directory.
const dirTailscale = "tailscale"

// TailscaleStatus is what the admin page and the banner show about Tailscale.
type TailscaleStatus struct {
	// Info is what Tailscale reports about this machine.
	tailnet.Info
	// URL is the HTTPS address on the MagicDNS name, "" without one.
	URL string
	// CertEnabled is --tailscale-cert.
	CertEnabled bool
	// CertActive is true while the Tailscale certificate is being served.
	CertActive bool
	// CertNotAfter is when the served Tailscale certificate expires.
	CertNotAfter time.Time
	// CertError says why the Tailscale certificate is not served, when
	// CertEnabled and not CertActive.
	CertError string
	// CACoversName is true when the local CA may sign the MagicDNS name,
	// so the name works with the local CA too.
	CACoversName bool
}

// tailscaleState remembers the last attempt to get the Tailscale certificate.
type tailscaleState struct {
	mu      sync.Mutex
	certErr string
}

// tailnetInfo asks Tailscale about this machine (cached by the client).
func (a *App) tailnetInfo(ctx context.Context) tailnet.Info {
	info, err := a.opts.Tailnet.Status(ctx)
	if err != nil {
		a.logger.Debug("tailscale status", "error", err.Error())
		return tailnet.Info{}
	}
	return info
}

// refreshTailscaleCert fetches (or renews) the Tailscale certificate for
// this machine's MagicDNS name and serves it to handshakes for that name,
// when --tailscale-cert is on. Failures are remembered for the admin page
// and logged; the local-CA certificate keeps serving everything else.
func (a *App) refreshTailscaleCert(ctx context.Context) error {
	if !a.cfg.TailscaleCert {
		return nil
	}
	setErr := func(msg string) {
		a.ts.mu.Lock()
		changed := a.ts.certErr != msg
		a.ts.certErr = msg
		a.ts.mu.Unlock()
		if changed && msg != "" {
			a.logger.Warn("tailscale certificate not served", "reason", msg)
		}
	}
	info := a.tailnetInfo(ctx)
	switch {
	case !info.Running:
		setErr("Tailscale is not running on this machine")
		return nil
	case info.DNSName == "":
		setErr("MagicDNS is off in this tailnet, so this machine has no Tailscale name")
		return nil
	case !info.CanCert():
		setErr("HTTPS certificates are not enabled in this tailnet (Tailscale admin console, DNS, HTTPS Certificates)")
		return nil
	}
	dir := filepath.Join(a.cfg.DataDir, dirTailscale)
	certFile := filepath.Join(dir, info.DNSName+".crt")
	keyFile := filepath.Join(dir, info.DNSName+".key")
	if err := a.opts.Tailnet.Cert(ctx, info.DNSName, certFile, keyFile); err != nil {
		msg := err.Error()
		if strings.Contains(strings.ToLower(msg), "access denied") || strings.Contains(strings.ToLower(msg), "permission") {
			msg += " (on Linux, run `sudo tailscale set --operator=<user running bubbles>` once)"
		}
		setErr(msg)
		return nil
	}
	m := a.Certs()
	if st := m.Status().SNI; st != nil && len(st.Names) == 1 && st.Names[0] == info.DNSName {
		_, err := m.Check() // same name: reload the files if tailscale renewed them
		setErr("")
		return err
	}
	if err := m.SetSNICert([]string{info.DNSName}, certFile, keyFile); err != nil {
		setErr(err.Error())
		return nil
	}
	setErr("")
	return nil
}

// Tailscale reports Tailscale's view of this machine and the certificate
// state, for the admin page and the banner.
func (a *App) Tailscale(ctx context.Context) TailscaleStatus {
	info := a.tailnetInfo(ctx)
	st := TailscaleStatus{Info: info, CertEnabled: a.cfg.TailscaleCert}
	if info.DNSName != "" {
		st.URL = baseURLs("https", []string{info.DNSName}, a.HTTPSPort())[0]
		if ca := a.Certs().CA(); ca != nil {
			st.CACoversName = caPermits(ca.Cert.PermittedDNSDomains, info.DNSName)
		}
	}
	if sni := a.Certs().Status().SNI; sni != nil {
		st.CertActive, st.CertNotAfter = true, sni.NotAfter
	}
	if st.CertEnabled && !st.CertActive {
		a.ts.mu.Lock()
		st.CertError = a.ts.certErr
		a.ts.mu.Unlock()
		if st.CertError == "" {
			st.CertError = "not fetched yet"
		}
	}
	return st
}

// caPermits reports whether name falls under one of the permitted DNS
// constraints (the name itself or a subdomain).
func caPermits(permitted []string, name string) bool {
	for _, d := range permitted {
		d = strings.ToLower(strings.TrimPrefix(d, "."))
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

// adminTailscale is the Tailscale part of the admin TLS status, nil when
// the server is not on a tailnet and --tailscale-cert is off.
func (a *App) adminTailscale() *protocol.AdminTailscale {
	st := a.Tailscale(context.Background())
	if !st.Running && !st.CertEnabled {
		return nil
	}
	out := &protocol.AdminTailscale{
		Running: st.Running, DNSName: st.DNSName, URL: st.URL, IPs: []string{},
		CertEnabled: st.CertEnabled, CertActive: st.CertActive, CertError: st.CertError,
		CACoversName: st.CACoversName,
	}
	for _, ip := range st.IPs {
		out.IPs = append(out.IPs, ip.String())
	}
	if st.CertActive {
		out.CertNotAfter = protocol.FormatTime(st.CertNotAfter.UnixMilli())
	}
	return out
}
