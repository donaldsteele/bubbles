package tlsca

import (
	"crypto/tls"
	"slices"
	"strings"
	"time"
)

// sniCert is a certificate served only to handshakes that ask for one of
// its names (TLS SNI), next to the main certificate: the publicly trusted
// certificate Tailscale issues for the server's MagicDNS name. Its files
// are reloaded when they change, like a bring-your-own pair.
type sniCert struct {
	names []string // lower case
	src   *byoSource
	pair  *tls.Certificate
}

// SNIStatus describes the certificate served by name (Status.SNI).
type SNIStatus struct {
	Names       []string  `json:"names"`
	Fingerprint string    `json:"fingerprint"`
	NotAfter    time.Time `json:"not_after"`
}

// SetSNICert serves the certificate in certFile and keyFile to TLS
// handshakes whose server name is one of names, and the main certificate
// to all others. It replaces an earlier one. The files are reloaded by
// Check when they change. An expired certificate is not served.
func (m *Manager) SetSNICert(names []string, certFile, keyFile string) error {
	src := &byoSource{certPath: certFile, keyPath: keyFile}
	pair, err := src.load()
	if err != nil {
		return err
	}
	lower := make([]string, 0, len(names))
	for _, n := range names {
		lower = append(lower, strings.ToLower(strings.TrimSuffix(n, ".")))
	}
	m.sni.Store(&sniCert{names: lower, src: src, pair: pair})
	m.logger.Info("serving TLS certificate by name", "names", strings.Join(lower, ","),
		"not_after", pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

// ClearSNICert stops serving the certificate set by SetSNICert.
func (m *Manager) ClearSNICert() { m.sni.Store(nil) }

// reloadSNI reloads the by-name certificate when its files changed (a
// broken pair keeps the old one in service). It reports whether it was
// replaced.
func (m *Manager) reloadSNI() (bool, error) {
	s := m.sni.Load()
	if s == nil {
		return false, nil
	}
	changed, err := s.src.changed()
	if err != nil || !changed {
		return false, err
	}
	src := &byoSource{certPath: s.src.certPath, keyPath: s.src.keyPath}
	pair, err := src.load()
	if err != nil {
		return false, err
	}
	m.sni.CompareAndSwap(s, &sniCert{names: s.names, src: src, pair: pair})
	m.logger.Info("reloaded TLS certificate by name", "names", strings.Join(s.names, ","),
		"not_after", pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return true, nil
}

// sniFor returns the by-name certificate for a handshake asking for
// serverName, or nil.
func (m *Manager) sniFor(serverName string) *tls.Certificate {
	s := m.sni.Load()
	if s == nil || serverName == "" {
		return nil
	}
	name := strings.ToLower(strings.TrimSuffix(serverName, "."))
	if !slices.Contains(s.names, name) {
		return nil
	}
	now := m.now()
	if leaf := s.pair.Leaf; leaf == nil || now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil
	}
	return s.pair
}

func (m *Manager) sniStatus() *SNIStatus {
	s := m.sni.Load()
	if s == nil || s.pair.Leaf == nil {
		return nil
	}
	return &SNIStatus{Names: slices.Clone(s.names), Fingerprint: Fingerprint(s.pair.Leaf.Raw), NotAfter: s.pair.Leaf.NotAfter.UTC()}
}
