package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/donaldsteele/bubbles/internal/tailnet"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

// fakeTailnet is a TailnetClient for a machine on a tailnet. Cert writes a
// certificate for the name, issued by its own stand-in "public" CA.
type fakeTailnet struct {
	mu      sync.Mutex
	info    tailnet.Info
	certErr error
	issuer  *tlsca.CA
	certs   int
}

func newFakeTailnet(t *testing.T, canCert bool) *fakeTailnet {
	t.Helper()
	issuer, err := tlsca.NewCA(tlsca.CAOptions{InstanceName: "Fake Public", ExtraDNS: []string{"tail1234.ts.net"}})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeTailnet{issuer: issuer, info: tailnet.Info{
		Running: true, DNSName: "chat-pi.tail1234.ts.net",
		IPs: []netip.Addr{netip.MustParseAddr("100.101.102.103")},
	}}
	if canCert {
		f.info.CertDomains = []string{"chat-pi.tail1234.ts.net"}
	}
	return f
}

func (f *fakeTailnet) Status(context.Context) (tailnet.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, nil
}

func (f *fakeTailnet) Cert(_ context.Context, domain, certFile, keyFile string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.certErr != nil {
		return f.certErr
	}
	f.certs++
	pair, err := f.issuer.Issue(tlsca.SANs{DNS: []string{domain}}, time.Now(), nil)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(strings.TrimSuffix(certFile, domain+".crt"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func leafFor(t *testing.T, h *harness, serverName string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", h.app.HTTPSAddr().String(), &tls.Config{InsecureSkipVerify: true, ServerName: serverName})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

// TestTailscaleNameInNewCA: a CA created while on a tailnet permits the
// machine's MagicDNS name (and nothing broader), and the leaf carries it.
func TestTailscaleNameInNewCA(t *testing.T) {
	ts := newFakeTailnet(t, false)
	h := startWith(t, testConfig(t), func(o *Options) { o.Tailnet = ts })
	defer h.stop(t)

	ca := h.app.Certs().CA().Cert
	if !slices.Contains(ca.PermittedDNSDomains, "chat-pi.tail1234.ts.net") || slices.Contains(ca.PermittedDNSDomains, "ts.net") ||
		slices.Contains(ca.PermittedDNSDomains, "tail1234.ts.net") {
		t.Fatalf("CA constraints %v", ca.PermittedDNSDomains)
	}
	leaf := servedLeaf(t, h)
	if !slices.Contains(leaf.DNSNames, "chat-pi.tail1234.ts.net") {
		t.Fatalf("leaf names %v", leaf.DNSNames)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "chat-pi.tail1234.ts.net"}); err != nil {
		t.Fatalf("leaf does not verify for the tailnet name: %v", err)
	}
	st := h.app.Tailscale(context.Background())
	if !st.CACoversName || st.URL == "" || st.CertEnabled {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(h.stderr.String(), "Tailscale:") || !strings.Contains(h.stderr.String(), "https://chat-pi.tail1234.ts.net:") {
		t.Fatalf("banner has no Tailscale line:\n%s", h.stderr.String())
	}
}

// TestTailscaleNameLeftOutOfOldCA: a CA created before the machine joined
// the tailnet does not permit the name, so the leaf leaves it out.
func TestTailscaleNameLeftOutOfOldCA(t *testing.T) {
	cfg := testConfig(t)
	h := start(t, cfg) // not on a tailnet: CA without the name
	h.stop(t)
	ts := newFakeTailnet(t, false)
	h2 := startWith(t, cfg, func(o *Options) { o.Tailnet = ts })
	defer h2.stop(t)
	if slices.Contains(servedLeaf(t, h2).DNSNames, "chat-pi.tail1234.ts.net") {
		t.Fatal("leaf carries a name its CA may not sign")
	}
	if st := h2.app.Tailscale(context.Background()); st.CACoversName {
		t.Fatalf("status %+v", st)
	}
}

// TestTailscaleCertServedBySNI: with --tailscale-cert the Tailscale
// certificate answers handshakes for the MagicDNS name and the local CA
// answers everything else; the admin status and onboarding page say so.
func TestTailscaleCertServedBySNI(t *testing.T) {
	ts := newFakeTailnet(t, true)
	cfg := testConfig(t)
	cfg.TailscaleCert = true
	h := startWith(t, cfg, func(o *Options) { o.Tailnet = ts })
	defer h.stop(t)
	if err := h.app.refreshTailscaleCert(context.Background()); err != nil {
		t.Fatal(err)
	}
	leaf := leafFor(t, h, "chat-pi.tail1234.ts.net")
	if leaf.Issuer.CommonName != ts.issuer.Cert.Subject.CommonName {
		t.Fatalf("tailnet name got a certificate from %q", leaf.Issuer.CommonName)
	}
	if other := leafFor(t, h, "localhost"); other.Issuer.CommonName != h.app.Certs().CA().Cert.Subject.CommonName {
		t.Fatalf("other names got a certificate from %q", other.Issuer.CommonName)
	}
	st := h.app.Tailscale(context.Background())
	if !st.CertActive || st.CertError != "" || st.CertNotAfter.IsZero() {
		t.Fatalf("status %+v", st)
	}
	adm := h.app.adminTailscale()
	if adm == nil || !adm.CertActive || adm.DNSName != "chat-pi.tail1234.ts.net" || len(adm.IPs) != 1 || adm.CertNotAfter == "" {
		t.Fatalf("admin %+v", adm)
	}
	// A second refresh with the same name reuses the files; no new SetSNICert.
	if err := h.app.refreshTailscaleCert(context.Background()); err != nil {
		t.Fatal(err)
	}

	resp, err := plainClient().Get("http://" + h.app.HTTPAddr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "On Tailscale?") || !strings.Contains(string(body), "skip the certificate step") {
		t.Fatalf("onboarding page has no trusted Tailscale address:\n%s", body)
	}
}

// TestTailscaleCertProblemsReported: when the tailnet cannot issue the
// certificate, the reason is reported and the local CA keeps serving.
func TestTailscaleCertProblemsReported(t *testing.T) {
	ts := newFakeTailnet(t, false) // HTTPS certificates off
	cfg := testConfig(t)
	cfg.TailscaleCert = true
	h := startWith(t, cfg, func(o *Options) { o.Tailnet = ts })
	defer h.stop(t)
	_ = h.app.refreshTailscaleCert(context.Background())
	st := h.app.Tailscale(context.Background())
	if st.CertActive || !strings.Contains(st.CertError, "HTTPS certificates are not enabled") {
		t.Fatalf("status %+v", st)
	}

	ts.mu.Lock()
	ts.info.CertDomains = []string{ts.info.DNSName}
	ts.certErr = errors.New("tailscale cert: exit status 1: Access denied: cert access denied")
	ts.mu.Unlock()
	_ = h.app.refreshTailscaleCert(context.Background())
	if st := h.app.Tailscale(context.Background()); st.CertActive || !strings.Contains(st.CertError, "--operator") {
		t.Fatalf("permission problem: %+v", st)
	}

	ts.mu.Lock()
	ts.info = tailnet.Info{}
	ts.mu.Unlock()
	_ = h.app.refreshTailscaleCert(context.Background())
	if st := h.app.Tailscale(context.Background()); !strings.Contains(st.CertError, "not running") {
		t.Fatalf("not running: %+v", st)
	}
	if h.app.adminTailscale() == nil {
		t.Fatal("admin status hidden while --tailscale-cert is on")
	}
}

// TestNoTailnetNoTailscaleStatus: off a tailnet and without the flag, the
// admin status has no Tailscale part and the page no Tailscale section.
func TestNoTailnetNoTailscaleStatus(t *testing.T) {
	h := start(t, testConfig(t))
	defer h.stop(t)
	if h.app.adminTailscale() != nil {
		t.Fatal("Tailscale status off a tailnet")
	}
	resp, err := plainClient().Get("http://" + h.app.HTTPAddr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "On Tailscale?") {
		t.Fatal("Tailscale section off a tailnet")
	}
}
