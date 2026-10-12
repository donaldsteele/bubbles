// Package discovery advertises the server over mDNS (DNS-SD): a
// _bubbles._tcp service instance on the HTTPS port plus A/AAAA records for
// <mdns-name>.local on the ranked LAN interfaces.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/brutella/dnssd"

	"github.com/donaldsteele/bubbles/internal/netutil"
)

// ServiceType is the DNS-SD service type browsed by the SDK and the tray.
const ServiceType = "_bubbles._tcp"

// TXTVersion is the value of the "v" TXT key.
const TXTVersion = "1"

// Fingerprint16 returns the first 16 lowercase hex digits of the SHA-256 of
// the CA certificate DER, or "" when der is empty (bring-your-own TLS).
func Fingerprint16(der []byte) string {
	if len(der) == 0 {
		return ""
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])[:16]
}

// TXT builds the TXT record map: v=1, fp=<16 hex>, onboard=<http port>, path=/.
func TXT(fp string, onboardPort int) map[string]string {
	return map[string]string{
		"v":       TXTVersion,
		"fp":      fp,
		"onboard": strconv.Itoa(onboardPort),
		"path":    "/",
	}
}

// Options configures an Advertiser.
type Options struct {
	// Instance is the service instance name (the instance name setting).
	Instance string
	// Host is the single-label mDNS host name; <Host>.local is published.
	Host string
	// HTTPSPort and HTTPPort are the bound listener ports.
	HTTPSPort, HTTPPort int
	// CADER returns the current CA certificate DER; nil or empty omits fp.
	CADER func() []byte
	// Ifaces returns the interface names to publish on; nil means the ranked
	// LAN interfaces. An empty result lets dnssd use every multicast interface.
	Ifaces func() []string
	// OnRename is called when probing had to rename the host or instance
	// (name conflict). The composition root triggers a TLS SAN refresh.
	OnRename func()
	// Logger receives diagnostics; nil discards.
	Logger *slog.Logger
}

// Advertiser owns one mDNS responder.
type Advertiser struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	resp   dnssd.Responder
	handle dnssd.ServiceHandle
	cancel context.CancelFunc
	done   chan struct{}
	host   string
	inst   string
}

// New returns an idle Advertiser.
func New(opts Options) *Advertiser {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Advertiser{opts: opts, log: log}
}

// LANIfaces returns the names of interfaces carrying ranked private LAN
// addresses.
func LANIfaces() []string {
	cands, err := netutil.Candidates()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, c := range cands {
		if !seen[c.Interface] {
			seen[c.Interface] = true
			names = append(names, c.Interface)
		}
	}
	return names
}

func (a *Advertiser) text() map[string]string {
	var fp string
	if a.opts.CADER != nil {
		fp = Fingerprint16(a.opts.CADER())
	}
	return TXT(fp, a.opts.HTTPPort)
}

// Start probes for unique names, then announces the service until ctx is
// cancelled or Close is called. It blocks for the probe only; responding
// continues in the background. A name conflict renames the instance or host
// (dnssd appends -2) and calls OnRename.
func (a *Advertiser) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resp != nil {
		return errors.New("discovery: already started")
	}
	if a.opts.HTTPSPort <= 0 {
		return errors.New("discovery: no HTTPS port")
	}
	ifaces := a.opts.Ifaces
	if ifaces == nil {
		ifaces = LANIfaces
	}
	svc, err := dnssd.NewService(dnssd.Config{
		Name:   a.opts.Instance,
		Type:   ServiceType,
		Host:   a.opts.Host,
		Text:   a.text(),
		Port:   a.opts.HTTPSPort,
		Ifaces: ifaces(),
	})
	if err != nil {
		return fmt.Errorf("discovery: build service: %w", err)
	}
	probed, err := dnssd.ProbeService(ctx, svc)
	if err != nil {
		return fmt.Errorf("discovery: probe names: %w", err)
	}
	renamed := probed.Host != svc.Host || probed.Name != svc.Name
	resp, err := dnssd.NewResponder()
	if err != nil {
		return fmt.Errorf("discovery: new responder: %w", err)
	}
	h, err := resp.Add(probed)
	if err != nil {
		return fmt.Errorf("discovery: add service: %w", err)
	}
	rctx, cancel := context.WithCancel(ctx)
	a.resp, a.handle, a.cancel, a.done = resp, h, cancel, make(chan struct{})
	a.host, a.inst = probed.Host, probed.Name
	go func(done chan struct{}) {
		defer close(done)
		if err := resp.Respond(rctx); err != nil && !errors.Is(err, context.Canceled) {
			a.log.Warn("mdns responder stopped", "error", err.Error())
		}
	}(a.done)
	if renamed {
		a.log.Info("mdns name conflict, renamed", "host", probed.Hostname(), "instance", probed.Name)
		if a.opts.OnRename != nil {
			go a.opts.OnRename()
		}
	}
	return nil
}

// Names returns the host and instance names actually in use after probing.
func (a *Advertiser) Names() (host, instance string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.host, a.inst
}

// Refresh re-announces the TXT record with the current CA fingerprint. Call
// it when the CA changes (restore, factory reset).
func (a *Advertiser) Refresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.handle == nil {
		return
	}
	a.handle.UpdateText(a.text(), a.resp)
}

// Close stops responding and waits for the responder to exit.
func (a *Advertiser) Close() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.resp, a.handle, a.cancel, a.done = nil, nil, nil, nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
