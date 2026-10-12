package tlsca

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// byoSource is a bring-your-own certificate and key pair (--tls-cert and
// --tls-key), reloaded when either file changes.
type byoSource struct {
	certPath, keyPath string
	stamp             pairStamp
}

// pairStamp identifies a version of the two files by size and mtime.
type pairStamp struct {
	certMod, keyMod   int64
	certSize, keySize int64
}

func statPair(certPath, keyPath string) (pairStamp, error) {
	ci, err := os.Stat(certPath)
	if err != nil {
		return pairStamp{}, err
	}
	ki, err := os.Stat(keyPath)
	if err != nil {
		return pairStamp{}, err
	}
	return pairStamp{certMod: ci.ModTime().UnixNano(), keyMod: ki.ModTime().UnixNano(), certSize: ci.Size(), keySize: ki.Size()}, nil
}

// load reads the pair and records its stamp. The certificate file may hold
// the full chain; the first block is the leaf.
func (b *byoSource) load() (*tls.Certificate, error) {
	stamp, err := statPair(b.certPath, b.keyPath)
	if err != nil {
		return nil, fmt.Errorf("tls certificate: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(b.certPath, b.keyPath)
	if err != nil {
		return nil, fmt.Errorf("tls certificate: %w", err)
	}
	if pair.Leaf == nil {
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, fmt.Errorf("tls certificate: %w", err)
		}
		pair.Leaf = leaf
	}
	b.stamp = stamp
	return &pair, nil
}

// changed reports whether either file differs from the loaded version.
func (b *byoSource) changed() (bool, error) {
	stamp, err := statPair(b.certPath, b.keyPath)
	if err != nil {
		return false, fmt.Errorf("tls certificate: %w", err)
	}
	return stamp != b.stamp, nil
}

// LoadBYO reads a bring-your-own certificate pair (used by `bubbles cert show`).
func LoadBYO(certPath, keyPath string) (*tls.Certificate, error) {
	b := &byoSource{certPath: certPath, keyPath: keyPath}
	return b.load()
}
