package bubblesclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

// publicChain makes a stand-in for a public CA (as if in the system roots)
// and a leaf it issued for names.
func publicChain(t *testing.T, names ...string) (root, leaf *x509.Certificate) {
	t.Helper()
	now := time.Now()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Public Test Root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ = x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, root, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ = x509.ParseCertificate(leafDER)
	return root, leaf
}

// TestPinAcceptsTailscaleCertForTailnetNames: a publicly trusted
// certificate is accepted for a *.ts.net machine name even though it does
// not chain to the pinned bubbles CA, and never for any other host.
func TestPinAcceptsTailscaleCertForTailnetNames(t *testing.T) {
	root, leaf := publicChain(t, "chat-pi.tail1234.ts.net", "files.example.com", "ts.net", "x.ts.net")
	old := systemRoots
	systemRoots = func() (*x509.CertPool, error) {
		p := x509.NewCertPool()
		p.AddCert(root)
		return p, nil
	}
	t.Cleanup(func() { systemRoots = old })
	bubblesCA := sha256.Sum256([]byte("some other CA"))
	chain := []*x509.Certificate{leaf}
	now := time.Now()

	if err := verifyPinned(chain, "chat-pi.tail1234.ts.net", bubblesCA[:], now); err != nil {
		t.Fatalf("tailnet name with a system-trusted certificate: %v", err)
	}
	if err := verifyPinned(chain, "Chat-Pi.tail1234.ts.net.", bubblesCA[:], now); err != nil {
		t.Fatalf("tailnet name, other case and trailing dot: %v", err)
	}
	for _, host := range []string{"files.example.com", "ts.net", "x.ts.net"} {
		if err := verifyPinned(chain, host, bubblesCA[:], now); !errors.Is(err, errPinMismatch) {
			t.Errorf("%s: %v, want a pin mismatch", host, err)
		}
	}
	// Not in the system roots: refused even for a tailnet name.
	_, stranger := publicChain(t, "chat-pi.tail1234.ts.net")
	if err := verifyPinned([]*x509.Certificate{stranger}, "chat-pi.tail1234.ts.net", bubblesCA[:], now); !errors.Is(err, errPinMismatch) {
		t.Fatalf("untrusted chain for a tailnet name: %v", err)
	}
	// Expired: refused.
	if err := verifyPinned(chain, "chat-pi.tail1234.ts.net", bubblesCA[:], now.Add(48*time.Hour)); !errors.Is(err, errPinMismatch) {
		t.Fatalf("expired certificate: %v", err)
	}
	// Valid for another tailnet machine only: refused.
	if err := verifyPinned(chain, "other.tail1234.ts.net", bubblesCA[:], now); !errors.Is(err, errPinMismatch) {
		t.Fatalf("certificate for another machine: %v", err)
	}
}
