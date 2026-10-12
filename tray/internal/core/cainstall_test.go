package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCAInstallFor(t *testing.T) {
	abs := func(parts ...string) string { return filepath.Join(append([]string{t.TempDir()}, parts...)...) }
	cert := abs("bubbles-ca.crt")
	home := abs("home")
	tests := []struct {
		goos string
		want *Command
		text string
	}{
		{"windows", &Command{Name: "certutil", Args: []string{"-user", "-addstore", "Root", cert}}, "personal trusted roots"},
		{"darwin", &Command{Name: "security", Args: []string{"add-trusted-cert", "-r", "trustRoot", "-k", filepath.Join(home, "Library", "Keychains", "login.keychain-db"), cert}}, "login keychain"},
		{"linux", nil, "update-ca-certificates"},
	}
	for _, tc := range tests {
		got, err := CAInstallFor(tc.goos, cert, home)
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if !reflect.DeepEqual(got.Command, tc.want) {
			t.Errorf("%s: command = %+v, want %+v", tc.goos, got.Command, tc.want)
		}
		if got.Instructions == "" || !strings.Contains(got.Instructions, tc.text) {
			t.Errorf("%s: instructions = %q", tc.goos, got.Instructions)
		}
	}
	for _, bad := range []struct{ goos, cert, home string }{
		{"windows", "relative.crt", home},
		{"windows", "/x\n/y", home},
		{"darwin", cert, ""},
		{"darwin", cert, "home"},
		{"plan9", cert, home},
	} {
		if _, err := CAInstallFor(bad.goos, bad.cert, bad.home); err == nil {
			t.Errorf("CAInstallFor(%+v) accepted", bad)
		}
	}
	// A path that looks like a flag must not reach the command line as one:
	// only absolute paths are accepted, so it always starts with a separator
	// or a drive letter.
	if _, err := CAInstallFor("windows", "-addstore", home); err == nil {
		t.Error("option-looking path accepted")
	}
}

func testCA(t *testing.T, isCA bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestWriteCAPEMOnlyWritesTheConfirmedCA(t *testing.T) {
	dir := t.TempDir()
	der := testCA(t, true)
	confirmed := FingerprintOf(der)

	if _, err := WriteCAPEM(dir, der, fpA); !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("wrong fingerprint: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("a certificate was written without confirmation")
	}
	if _, err := WriteCAPEM(dir, der, "nonsense"); err == nil {
		t.Fatal("bad fingerprint accepted")
	}
	notCA := testCA(t, false)
	if _, err := WriteCAPEM(dir, notCA, FingerprintOf(notCA)); err == nil {
		t.Fatal("a leaf certificate was accepted as CA")
	}

	path, err := WriteCAPEM(dir, der, GroupFingerprint(confirmed))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" || FingerprintOf(blk.Bytes) != confirmed {
		t.Fatal("written PEM does not hold the confirmed certificate")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("path %q is not absolute", path)
	}
}
