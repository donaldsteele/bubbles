package tlsca

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Mode says where the served certificate comes from.
type Mode string

// Certificate modes.
const (
	ModeLocalCA Mode = "local-ca"
	ModeBYO     Mode = "byo"
)

// ErrBYO is returned by Manager.Reissue in bring-your-own mode.
var ErrBYO = errors.New("the server uses --tls-cert/--tls-key; replace those files instead")

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	// Dir is the data directory's tls/ folder (CA and leaf files).
	Dir string
	// CertFile and KeyFile select bring-your-own mode when both are set.
	CertFile, KeyFile string
	// CA configures the CA when Dir has none yet.
	CA CAOptions
	// SANs returns the desired leaf SAN set (see Discover). Required in
	// local-CA mode.
	SANs func() (SANs, error)
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Rand is the entropy source; nil means crypto/rand.
	Rand io.Reader
	// Logger receives issuance and reload notices; nil discards them.
	Logger *slog.Logger
}

// Status describes the served certificate (admin UI and `bubbles cert show`).
type Status struct {
	Mode            Mode      `json:"mode"`
	CAFingerprint   string    `json:"ca_fingerprint,omitempty"`
	LeafFingerprint string    `json:"leaf_fingerprint"`
	LeafNotAfter    time.Time `json:"leaf_not_after"`
	SANs            []string  `json:"sans"`
}

// Manager owns the served certificate: it issues and renews the leaf from
// the local CA (or reloads a bring-your-own pair) and hands the current one
// to TLS handshakes through GetCertificate, so swaps need no restart.
type Manager struct {
	opts   ManagerOptions
	now    func() time.Time
	logger *slog.Logger

	mu  sync.Mutex // serializes Check and Reissue
	ca  *CA        // nil in BYO mode; immutable after NewManager
	byo *byoSource
	cur atomic.Pointer[tls.Certificate]
}

// NewManager loads (or creates) the CA and a valid leaf, or loads the
// bring-your-own pair. On return GetCertificate is ready.
func NewManager(opts ManagerOptions) (*Manager, error) {
	m := &Manager{opts: opts, now: opts.Now, logger: opts.Logger}
	if m.now == nil {
		m.now = time.Now
	}
	if m.logger == nil {
		m.logger = slog.New(slog.DiscardHandler)
	}
	if opts.CertFile != "" || opts.KeyFile != "" {
		if opts.CertFile == "" || opts.KeyFile == "" {
			return nil, errors.New("tls: certificate and key files must be given together")
		}
		m.byo = &byoSource{certPath: opts.CertFile, keyPath: opts.KeyFile}
		pair, err := m.byo.load()
		if err != nil {
			return nil, err
		}
		m.store(pair)
		m.warnIfExpired(pair.Leaf)
		return m, nil
	}

	if opts.SANs == nil {
		return nil, errors.New("tls: SAN source is required")
	}
	caOpts := opts.CA
	if caOpts.Now == nil {
		caOpts.Now = m.now
	}
	if caOpts.Rand == nil {
		caOpts.Rand = opts.Rand
	}
	ca, created, err := LoadOrCreateCA(opts.Dir, caOpts)
	if err != nil {
		return nil, err
	}
	m.ca = ca
	if created {
		m.logger.Info("created local CA", "subject", ca.Cert.Subject.CommonName, "fingerprint", ca.Fingerprint())
	}
	if leaf, err := LoadLeaf(opts.Dir); err == nil {
		m.store(leaf)
	}
	if _, err := m.check(false); err != nil {
		return nil, err
	}
	return m, nil
}

// Mode reports local-CA or bring-your-own mode.
func (m *Manager) Mode() Mode {
	if m.byo != nil {
		return ModeBYO
	}
	return ModeLocalCA
}

// CA returns the local CA, or nil in bring-your-own mode.
func (m *Manager) CA() *CA { return m.ca }

// Current returns the certificate being served.
func (m *Manager) Current() *tls.Certificate { return m.cur.Load() }

// GetCertificate implements tls.Config.GetCertificate.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := m.cur.Load()
	if c == nil {
		return nil, errors.New("tls: no certificate loaded")
	}
	return c, nil
}

// Check re-evaluates the served certificate. In local-CA mode it recomputes
// the SAN set and re-issues the leaf when the set changed or fewer than 30
// days remain; in bring-your-own mode it reloads the files when either one
// changed (a broken pair keeps the old one in service). It reports whether
// the served certificate was replaced.
func (m *Manager) Check() (bool, error) {
	return m.check(false)
}

// Reissue issues a new leaf now, whatever the state of the current one.
func (m *Manager) Reissue() error {
	if m.byo != nil {
		return ErrBYO
	}
	_, err := m.check(true)
	return err
}

func (m *Manager) check(force bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.byo != nil {
		changed, err := m.byo.changed()
		if err != nil || !changed {
			return false, err
		}
		pair, err := m.byo.load()
		if err != nil {
			return false, err
		}
		m.store(pair)
		m.logger.Info("reloaded TLS certificate", "not_after", pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
		m.warnIfExpired(pair.Leaf)
		return true, nil
	}

	want, err := m.opts.SANs()
	if err != nil {
		return false, fmt.Errorf("tls: discover SANs: %w", err)
	}
	want, dropped := Filter(m.ca.Cert, want)
	now := m.now()
	var leaf *x509.Certificate
	if c := m.cur.Load(); c != nil {
		leaf = c.Leaf
	}
	reason := reissueReason(m.ca, leaf, want, now)
	if force {
		reason = "requested"
	}
	if reason == "" {
		return false, nil
	}
	if len(dropped) > 0 {
		m.logger.Warn("names outside the CA name constraints left out of the certificate",
			"names", strings.Join(dropped, ","))
	}
	next, err := m.ca.Issue(want, now, m.opts.Rand)
	if err != nil {
		return false, err
	}
	if err := SaveLeaf(m.opts.Dir, next); err != nil {
		return false, err
	}
	m.store(next)
	m.logger.Info("issued TLS certificate", "reason", reason,
		"not_after", next.Leaf.NotAfter.UTC().Format(time.RFC3339), "sans", strings.Join(want.Strings(), ","))
	return true, nil
}

func (m *Manager) warnIfExpired(leaf *x509.Certificate) {
	if leaf != nil && m.now().After(leaf.NotAfter) {
		m.logger.Warn("TLS certificate has expired", "not_after", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
}

// Status describes the CA and the served certificate.
func (m *Manager) Status() Status {
	st := Status{Mode: m.Mode()}
	if m.ca != nil {
		st.CAFingerprint = m.ca.Fingerprint()
	}
	if c := m.cur.Load(); c != nil && c.Leaf != nil {
		st.LeafFingerprint = Fingerprint(c.Leaf.Raw)
		st.LeafNotAfter = c.Leaf.NotAfter.UTC()
		st.SANs = SANsOf(c.Leaf).Strings()
	}
	return st
}

// store makes c the served certificate. In local-CA mode the chain carries
// the CA after the leaf, so pinned clients (the SDK) can verify the server
// against a CA fingerprint alone.
func (m *Manager) store(c *tls.Certificate) {
	if m.ca != nil && c != nil && len(c.Certificate) == 1 {
		c = &tls.Certificate{Certificate: [][]byte{c.Certificate[0], m.ca.DER()}, PrivateKey: c.PrivateKey, Leaf: c.Leaf}
	}
	m.cur.Store(c)
}
