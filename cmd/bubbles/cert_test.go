package main

import (
	"bytes"
	"encoding/pem"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/app"
	"github.com/donaldsteele/bubbles/internal/config"
	"github.com/donaldsteele/bubbles/internal/tlsca"
)

func runCertTest(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	opts := config.Options{Getenv: func(k string) string { return env[k] }}
	appOpts := app.Options{
		LANAddrs: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("192.168.7.7")}, nil },
		Hostname: func() (string, error) { return "cert-host", nil },
	}
	code = certCmd(args, opts, appOpts, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCertUsageErrors(t *testing.T) {
	dir := t.TempDir()
	tests := [][]string{
		nil,
		{"nope"},
		{"show", "extra"},
		{"show", "--no-such-flag"},
		{"export-ca", "--data-dir", dir, "--format", "jks"},
		{"show", "--data-dir", dir, "--tls-cert", "only-cert.pem"},
		{"reissue", "--data-dir", dir, "--tls-extra-dns", "8.8.8.8"},
	}
	for _, args := range tests {
		if code, _, _ := runCertTest(t, nil, args...); code != exitUsage {
			t.Errorf("cert %q: exit = %d, want %d", args, code, exitUsage)
		}
	}
	if code, _, stderr := runCertTest(t, nil, "export-ca", "--help"); code != exitOK || !strings.Contains(stderr, "-format") {
		t.Errorf("--help: exit = %d, stderr = %q", code, stderr)
	}
}

func TestCertShowWithoutCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh")
	code, _, stderr := runCertTest(t, nil, "show", "--data-dir", dir)
	if code != exitRuntime || !strings.Contains(stderr, "no local CA") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("show created the data dir (stat err = %v)", err)
	}
}

func TestCertReissueShowExport(t *testing.T) {
	dir := t.TempDir()
	code, stdout, stderr := runCertTest(t, nil, "reissue", "--data-dir", dir, "--mdns-name", "chat")
	if code != exitOK || !strings.Contains(stdout, "issued a new certificate") {
		t.Fatalf("reissue: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	tlsDir := filepath.Join(dir, config.DirTLS)
	ca, err := tlsca.LoadCA(tlsDir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := tlsca.LoadLeafCert(tlsDir)
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, _ = runCertTest(t, map[string]string{"BUBBLES_DATA_DIR": dir}, "show")
	for _, want := range []string{"mode:             local-ca", ca.Fingerprint(), "leaf not after:", "192.168.7.7", "chat.local", "cert-host.local"} {
		if code != exitOK || !strings.Contains(stdout, want) {
			t.Errorf("show (exit %d) lacks %q:\n%s", code, want, stdout)
		}
	}

	if code, _, stderr := runCertTest(t, nil, "reissue", "--data-dir", dir, "--mdns-name", "chat"); code != exitOK {
		t.Fatalf("second reissue: exit = %d, %s", code, stderr)
	}
	second, err := tlsca.LoadLeafCert(tlsDir)
	if err != nil || second.SerialNumber.Cmp(first.SerialNumber) == 0 {
		t.Errorf("reissue kept the old leaf (%v)", err)
	}
	if again, err := tlsca.LoadCA(tlsDir); err != nil || again.Fingerprint() != ca.Fingerprint() {
		t.Errorf("reissue replaced the CA (%v)", err)
	}

	code, stdout, _ = runCertTest(t, nil, "export-ca", "--data-dir", dir)
	if block, _ := pem.Decode([]byte(stdout)); code != exitOK || block == nil || !bytes.Equal(block.Bytes, ca.DER()) {
		t.Errorf("export-ca pem: exit = %d", code)
	}
	code, stdout, _ = runCertTest(t, nil, "export-ca", "--data-dir", dir, "--format", "der")
	if code != exitOK || stdout != string(ca.DER()) {
		t.Errorf("export-ca der: exit = %d", code)
	}
	out := filepath.Join(t.TempDir(), "ca.mobileconfig")
	code, _, _ = runCertTest(t, nil, "export-ca", "--data-dir", dir, "--format", "mobileconfig", "--out", out)
	if got, err := os.ReadFile(out); code != exitOK || err != nil || !bytes.Equal(got, ca.MobileConfig()) {
		t.Errorf("export-ca mobileconfig: exit = %d, err = %v", code, err)
	}
}

func TestCertReissueRefusesWhileServerRuns(t *testing.T) {
	dir := t.TempDir()
	if err := config.EnsureDataDir(dir); err != nil {
		t.Fatal(err)
	}
	lock, err := config.AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if code, _, stderr := runCertTest(t, nil, "reissue", "--data-dir", dir); code != exitDirInUse || !strings.Contains(stderr, "stop it first") {
		t.Errorf("exit = %d, stderr = %q", code, stderr)
	}
}

func TestCertBYO(t *testing.T) {
	dir := t.TempDir()
	ca, err := tlsca.NewCA(tlsca.CAOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Issue(tlsca.SANs{DNS: []string{"chat.lan"}}, ca.Cert.NotBefore.AddDate(0, 0, 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tlsca.SaveLeaf(dir, leaf); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"BUBBLES_DATA_DIR": dir,
		"BUBBLES_TLS_CERT": filepath.Join(dir, tlsca.LeafCertFile),
		"BUBBLES_TLS_KEY":  filepath.Join(dir, tlsca.LeafKeyFile),
	}
	code, stdout, _ := runCertTest(t, env, "show")
	if code != exitOK || !strings.Contains(stdout, "mode:             byo") || !strings.Contains(stdout, "chat.lan") ||
		!strings.Contains(stdout, tlsca.Fingerprint(leaf.Leaf.Raw)) {
		t.Errorf("show: exit = %d, stdout = %q", code, stdout)
	}
	if code, _, _ := runCertTest(t, env, "reissue"); code != exitUsage {
		t.Errorf("reissue in BYO mode: exit = %d", code)
	}
}
