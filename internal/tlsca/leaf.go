package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LeafValidity is the lifetime of an issued leaf certificate (browsers cap
// locally trusted server certificates near 398 days).
const LeafValidity = 397 * 24 * time.Hour

// RenewBefore is how close to expiry a leaf gets re-issued.
const RenewBefore = 30 * 24 * time.Hour

// Issue signs a new serverAuth leaf for sans with a fresh ECDSA P-256 key.
// NotBefore is now - 1h. Names the CA's constraints do not permit must have
// been removed by the caller (see Filter).
func (ca *CA) Issue(sans SANs, now time.Time, rnd io.Reader) (*tls.Certificate, error) {
	if rnd == nil {
		rnd = rand.Reader
	}
	if len(sans.DNS) == 0 && len(sans.IPs) == 0 {
		return nil, errors.New("issue leaf: empty SAN set")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rnd)
	if err != nil {
		return nil, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial(rnd)
	if err != nil {
		return nil, err
	}
	notBefore := now.Add(-backdate).UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: leafCommonName(sans)},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(LeafValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              append([]string(nil), sans.DNS...),
	}
	for _, ip := range sans.IPs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	der, err := x509.CreateCertificate(rnd, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("issue leaf: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, nil
}

// leafCommonName picks a name the CA constraints permit: the first .local
// name, else the first DNS name, else empty.
func leafCommonName(sans SANs) string {
	for _, d := range sans.DNS {
		if strings.HasSuffix(d, ".local") {
			return d
		}
	}
	if len(sans.DNS) > 0 {
		return sans.DNS[0]
	}
	return ""
}

// SaveLeaf writes leaf.key, then leaf.crt into dir (0600).
func SaveLeaf(dir string, leaf *tls.Certificate) error {
	key, ok := leaf.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("save leaf: not an ECDSA key")
	}
	keyPEM, err := marshalKeyPEM(key)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, LeafKeyFile), keyPEM); err != nil {
		return fmt.Errorf("write leaf key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]})
	if err := writeFileAtomic(filepath.Join(dir, LeafCertFile), certPEM); err != nil {
		return fmt.Errorf("write leaf certificate: %w", err)
	}
	return nil
}

// LoadLeaf reads leaf.crt and leaf.key from dir.
func LoadLeaf(dir string) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, LeafCertFile))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, LeafKeyFile))
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("leaf: %w", err)
	}
	return &pair, nil
}

// LoadLeafCert reads only leaf.crt from dir.
func LoadLeafCert(dir string) (*x509.Certificate, error) {
	data, err := os.ReadFile(filepath.Join(dir, LeafCertFile))
	if err != nil {
		return nil, err
	}
	return parseCertPEM(data)
}

// reissueReason explains why leaf must be replaced, or returns "" when it is
// still good: it must be signed by ca, have more than RenewBefore left and
// carry exactly the SAN set want.
func reissueReason(ca *CA, leaf *x509.Certificate, want SANs, now time.Time) string {
	switch {
	case leaf == nil:
		return "no leaf certificate"
	case leaf.CheckSignatureFrom(ca.Cert) != nil:
		return "leaf not signed by the current CA"
	case leaf.NotAfter.Sub(now) < RenewBefore:
		return "leaf expires within 30 days"
	case !SANsOf(leaf).Equal(want):
		return "SAN set changed"
	}
	return ""
}
