// Package tlsca runs the built-in, name-constrained local certificate
// authority: CA create/load, leaf issue and renewal from the discovered SAN
// set, hot swap through tls.Config.GetCertificate, bring-your-own certificate
// reload, fingerprints and the iOS .mobileconfig profile.
package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside the data directory's tls/ folder.
const (
	CACertFile   = "ca.crt"
	CAKeyFile    = "ca.key"
	LeafCertFile = "leaf.crt"
	LeafKeyFile  = "leaf.key"
)

// CAValidity is the lifetime of a new CA certificate.
const CAValidity = 10 * 365 * 24 * time.Hour

// backdate is subtracted from NotBefore so devices with a slow clock accept
// fresh certificates.
const backdate = time.Hour

// ErrNoCA is returned by LoadCA when the directory holds no CA certificate.
var ErrNoCA = errors.New("no local CA in this data directory")

// CA is the local certificate authority.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// CAOptions are the inputs to NewCA.
type CAOptions struct {
	// InstanceName appears in the CA common name; "" means "bubbles".
	InstanceName string
	// Hostname is the OS host name, added to the permitted DNS constraints.
	Hostname string
	// ExtraDNS are the --tls-extra-dns names, added to the constraints.
	ExtraDNS []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Rand is the entropy source; nil means crypto/rand.
	Rand io.Reader
}

// NewCA creates a CA: ECDSA P-256, 10 years, CA:TRUE pathlen 0, key usage
// certSign|crlSign and critical name constraints (BaseDNSDomains, the host
// name and the extra names; PermittedIPRanges).
func NewCA(opts CAOptions) (*CA, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	rnd := opts.Rand
	if rnd == nil {
		rnd = rand.Reader
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rnd)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	var tag [2]byte
	if _, err := io.ReadFull(rnd, tag[:]); err != nil {
		return nil, fmt.Errorf("generate CA name: %w", err)
	}
	serial, err := randomSerial(rnd)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(opts.InstanceName)
	if name == "" {
		name = "bubbles"
	}
	if len(name) > 32 {
		name = name[:32]
	}
	notBefore := now().Add(-backdate).UTC().Truncate(time.Second)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("bubbles Local CA (%s %s)", name, hex.EncodeToString(tag[:])),
			Organization: []string{"bubbles"},
		},
		NotBefore:                   notBefore,
		NotAfter:                    notBefore.Add(CAValidity),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permittedDNS(opts.Hostname, opts.ExtraDNS),
		PermittedIPRanges:           permittedIPNets(),
	}
	der, err := x509.CreateCertificate(rnd, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, key: key}, nil
}

// LoadCA reads the CA from dir. It returns an error wrapping ErrNoCA when
// ca.crt does not exist, and refuses a certificate without its key.
func LoadCA(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w (%s)", ErrNoCA, dir)
	}
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, fmt.Errorf("read CA key: %w", err)
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("CA certificate: not a CA")
	}
	key, err := parseKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("CA key: %w", err)
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("CA key does not match the CA certificate")
	}
	return &CA{Cert: cert, key: key}, nil
}

// LoadOrCreateCA loads the CA from dir or, when there is none, creates one
// and saves it. created reports which happened.
func LoadOrCreateCA(dir string, opts CAOptions) (ca *CA, created bool, err error) {
	ca, err = LoadCA(dir)
	if err == nil || !errors.Is(err, ErrNoCA) {
		return ca, false, err
	}
	ca, err = NewCA(opts)
	if err != nil {
		return nil, false, err
	}
	if err := ca.Save(dir); err != nil {
		return nil, false, err
	}
	return ca, true, nil
}

// Save writes ca.key, then ca.crt (both 0600) into dir. A crash between the
// two leaves only a key behind, which LoadOrCreateCA replaces: the
// certificate was never served, so no device can trust it yet.
func (ca *CA) Save(dir string) error {
	keyPEM, err := marshalKeyPEM(ca.key)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, CAKeyFile), keyPEM); err != nil {
		return fmt.Errorf("write CA key: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, CACertFile), ca.PEM()); err != nil {
		return fmt.Errorf("write CA certificate: %w", err)
	}
	return nil
}

// DER returns the CA certificate in DER form.
func (ca *CA) DER() []byte { return ca.Cert.Raw }

// PEM returns the CA certificate in PEM form.
func (ca *CA) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw})
}

// Fingerprint returns the CA certificate's SHA-256 fingerprint (see Fingerprint).
func (ca *CA) Fingerprint() string { return Fingerprint(ca.Cert.Raw) }

// Fingerprint formats the SHA-256 of der as upper-case hex in groups of four,
// separated by spaces ("AB12 CD34 ...").
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	var b strings.Builder
	for i := 0; i < len(h); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(h[i : i+4])
	}
	return b.String()
}

func randomSerial(rnd io.Reader) (*big.Int, error) {
	serial, err := rand.Int(rnd, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}
	return serial.Add(serial, big.NewInt(1)), nil
}

func parseCertPEM(data []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no PEM CERTIFICATE block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("no PEM PRIVATE KEY block")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("not an ECDSA key")
	}
	return key, nil
}

func marshalKeyPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// writeFileAtomic writes data to a temporary file next to path and renames
// it into place, so readers never see a partial file. os.CreateTemp creates
// the file 0600; on Windows the data directory's protected ACL is inherited.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
