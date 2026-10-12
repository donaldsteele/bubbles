package core

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Command is a program and its arguments. It is always executed directly,
// never through a shell.
type Command struct {
	Name string
	Args []string
}

// CAInstall is how the per-user CA trust helper works on one OS.
type CAInstall struct {
	// Command installs the CA, or is nil when the OS needs manual steps.
	Command *Command
	// Instructions explains the manual steps (Linux) or what to expect.
	Instructions string
}

// CAInstallFor builds the helper for goos (a runtime.GOOS value) and the PEM
// file certPath. home is the user's home directory (macOS keychain path).
// The commands touch only the current user's trust store.
func CAInstallFor(goos, certPath, home string) (CAInstall, error) {
	if !filepath.IsAbs(certPath) {
		return CAInstall{}, errors.New("the certificate path must be absolute")
	}
	if strings.ContainsAny(certPath, "\x00\r\n") {
		return CAInstall{}, errors.New("invalid certificate path")
	}
	switch goos {
	case "windows":
		return CAInstall{
			Command: &Command{Name: "certutil", Args: []string{"-user", "-addstore", "Root", certPath}},
			Instructions: "Windows will ask you to confirm adding the certificate to your personal trusted roots. " +
				"Compare the fingerprint in that dialog with the one you confirmed.",
		}, nil
	case "darwin":
		if home == "" || !filepath.IsAbs(home) {
			return CAInstall{}, errors.New("the home directory is unknown")
		}
		keychain := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
		return CAInstall{
			Command:      &Command{Name: "security", Args: []string{"add-trusted-cert", "-r", "trustRoot", "-k", keychain, certPath}},
			Instructions: "macOS will ask for your password to change the trust settings of your login keychain.",
		}, nil
	case "linux":
		return CAInstall{
			Instructions: "Linux has no single per-user trust store. Add the certificate " + certPath + " to the " +
				"trust store your browser and webview use. For system-wide trust on Debian or Ubuntu: " +
				"sudo cp it to /usr/local/share/ca-certificates/bubbles.crt and run sudo update-ca-certificates. " +
				"Firefox and Chromium keep their own stores (Settings, Certificates, Authorities, Import).",
		}, nil
	}
	return CAInstall{}, fmt.Errorf("unsupported operating system %q", goos)
}

// WriteCAPEM verifies that der hashes to the confirmed fingerprint and is a
// CA certificate, then writes it as PEM into dir. Only a CA the user
// confirmed can ever reach the trust store.
func WriteCAPEM(dir string, der []byte, confirmed string) (string, error) {
	want, err := NormalizeFingerprint(confirmed)
	if err != nil {
		return "", err
	}
	if FingerprintOf(der) != want {
		return "", ErrFingerprintMismatch
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || !cert.IsCA {
		return "", errors.New("the data is not a CA certificate")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	path := filepath.Join(dir, "bubbles-ca.crt")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write certificate: %w", err)
	}
	return path, nil
}
