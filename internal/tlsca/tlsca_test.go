package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// sanSource is a settable SAN discovery function.
type sanSource struct {
	mu sync.Mutex
	in SANInput
}

func (s *sanSource) set(in SANInput) {
	s.mu.Lock()
	s.in = in
	s.mu.Unlock()
}

func (s *sanSource) get() (SANs, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Discover(s.in), nil
}

func testCA(t *testing.T, extra ...string) *CA {
	t.Helper()
	ca, err := NewCA(CAOptions{InstanceName: "Home", Hostname: "Desk-PC", ExtraDNS: extra, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func pool(ca *CA) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.Cert)
	return p
}

func TestNewCAProperties(t *testing.T) {
	ca := testCA(t, "chat.example.org")
	c := ca.Cert
	if !c.IsCA || !c.BasicConstraintsValid || c.MaxPathLen != 0 || !c.MaxPathLenZero {
		t.Errorf("basic constraints: IsCA=%v valid=%v pathlen=%d zero=%v", c.IsCA, c.BasicConstraintsValid, c.MaxPathLen, c.MaxPathLenZero)
	}
	if c.KeyUsage != x509.KeyUsageCertSign|x509.KeyUsageCRLSign {
		t.Errorf("key usage = %v", c.KeyUsage)
	}
	if _, ok := c.PublicKey.(*ecdsa.PublicKey); !ok || c.PublicKey.(*ecdsa.PublicKey).Curve != elliptic.P256() {
		t.Errorf("CA key is not ECDSA P-256")
	}
	if !regexp.MustCompile(`^bubbles Local CA \(Home [0-9a-f]{4}\)$`).MatchString(c.Subject.CommonName) {
		t.Errorf("CN = %q", c.Subject.CommonName)
	}
	if got := c.NotAfter.Sub(c.NotBefore); got != CAValidity {
		t.Errorf("validity = %v", got)
	}
	if !c.NotBefore.Equal(t0.Add(-time.Hour)) {
		t.Errorf("NotBefore = %v", c.NotBefore)
	}
	wantDNS := []string{"local", "home.arpa", "lan", "internal", "localdomain", "localhost", "desk-pc", "chat.example.org"}
	if strings.Join(c.PermittedDNSDomains, ",") != strings.Join(wantDNS, ",") {
		t.Errorf("permitted DNS = %v", c.PermittedDNSDomains)
	}
	if len(c.PermittedIPRanges) != len(PermittedIPRanges) {
		t.Errorf("permitted IPs = %v", c.PermittedIPRanges)
	}
	nameConstraints := asn1.ObjectIdentifier{2, 5, 29, 30}
	found := false
	for _, ext := range c.Extensions {
		if ext.Id.Equal(nameConstraints) {
			found = true
			if !ext.Critical {
				t.Error("name constraints extension is not critical")
			}
		}
	}
	if !found {
		t.Error("no name constraints extension")
	}
}

func TestLeafVerifiesAtEachSAN(t *testing.T) {
	ca := testCA(t)
	sans := Discover(SANInput{
		Addrs:    []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("100.101.1.2"), netip.MustParseAddr("fe80::1")},
		Hostname: "Desk-PC",
		MDNSName: "bubbles",
		Extra:    []string{"nas.lan"},
	})
	leaf, err := ca.Issue(sans, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := leaf.Leaf.NotAfter.Sub(leaf.Leaf.NotBefore); got != LeafValidity {
		t.Errorf("leaf validity = %v", got)
	}
	if !leaf.Leaf.NotBefore.Equal(t0.Add(-time.Hour)) {
		t.Errorf("leaf NotBefore = %v", leaf.Leaf.NotBefore)
	}
	names := sans.Strings()
	for _, want := range []string{"localhost", "desk-pc", "desk-pc.local", "bubbles.local", "nas.lan", "127.0.0.1", "::1", "192.168.1.20"} {
		if !strings.Contains(","+strings.Join(names, ",")+",", ","+want+",") {
			t.Errorf("SANs %v lack %q", names, want)
		}
	}
	for _, name := range names {
		_, err := leaf.Leaf.Verify(x509.VerifyOptions{
			DNSName:     name,
			Roots:       pool(ca),
			CurrentTime: t0,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			t.Errorf("Verify(%s): %v", name, err)
		}
	}
}

// rogueLeaf signs a leaf for arbitrary names with the CA key, bypassing Filter.
func rogueLeaf(t *testing.T, ca *CA, dns []string, ips []net.IP) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "rogue"},
		NotBefore:    t0.Add(-time.Hour),
		NotAfter:     t0.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestNameConstraintsRejectForeignNames(t *testing.T) {
	ca := testCA(t)
	tests := []struct {
		name string
		dns  []string
		ips  []net.IP
		host string
	}{
		{"public DNS name", []string{"example.com"}, nil, "example.com"},
		{"public IP", nil, []net.IP{net.ParseIP("8.8.8.8")}, "8.8.8.8"},
		{"public name next to a permitted one", []string{"bubbles.local", "example.com"}, nil, "bubbles.local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			leaf := rogueLeaf(t, ca, tt.dns, tt.ips)
			_, err := leaf.Verify(x509.VerifyOptions{DNSName: tt.host, Roots: pool(ca), CurrentTime: t0})
			var invalid x509.CertificateInvalidError
			if !errors.As(err, &invalid) || invalid.Reason != x509.CANotAuthorizedForThisName {
				t.Errorf("Verify error = %v, want CANotAuthorizedForThisName", err)
			}
		})
	}
}

func TestFilterDropsNamesOutsideConstraints(t *testing.T) {
	ca := testCA(t)
	in := SANs{
		DNS: []string{"localhost", "renamed-host", "renamed-host.local", "chat.example.org"},
		IPs: []netip.Addr{netip.MustParseAddr("192.168.1.2"), netip.MustParseAddr("203.0.113.9")},
	}
	ok, dropped := Filter(ca.Cert, in)
	if got := strings.Join(ok.Strings(), ","); got != "192.168.1.2,localhost,renamed-host.local" {
		t.Errorf("kept = %s", got)
	}
	if got := strings.Join(dropped, ","); got != "renamed-host,chat.example.org,203.0.113.9" {
		t.Errorf("dropped = %s", got)
	}
}

func TestValidDNSName(t *testing.T) {
	long := strings.Repeat("a", 63)
	tests := []struct {
		name string
		ok   bool
	}{
		{"nas.lan", true},
		{"chat.example.org", true},
		{"a", true},
		{"x-1.y2", true},
		{long + ".lan", true},
		{"", false},
		{"a..b", false},
		{".lan", false},
		{"-a.lan", false},
		{"a-.lan", false},
		{"a_b.lan", false},
		{"A.lan", false},
		{"*.lan", false},
		{long + "a.lan", false},
		{strings.Repeat("a.", 127) + "ab", false},
		{"10.0.0.1", false},
		{"::1", false},
	}
	for _, tt := range tests {
		if err := ValidDNSName(tt.name); (err == nil) != tt.ok {
			t.Errorf("ValidDNSName(%q) = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestFingerprintFormat(t *testing.T) {
	fp := Fingerprint([]byte("x"))
	if !regexp.MustCompile(`^([0-9A-F]{4} ){15}[0-9A-F]{4}$`).MatchString(fp) {
		t.Errorf("fingerprint = %q", fp)
	}
	if !strings.HasPrefix(fp, "2D71 1642 B726 B044") {
		t.Errorf("fingerprint of %q = %q", "x", fp)
	}
}

func TestDiscoverSANs(t *testing.T) {
	got := Discover(SANInput{
		Addrs:    []netip.Addr{netip.MustParseAddr("::ffff:192.168.1.2"), netip.MustParseAddr("fe80::1%eth0"), netip.MustParseAddr("192.168.1.2")},
		Hostname: "Mac.Local.",
		MDNSName: "chat",
		Extra:    []string{"NAS.lan.", "bad_name", "chat.local"},
	})
	want := "192.168.1.2,fe80::1,127.0.0.1,::1,localhost,mac.local,chat.local,nas.lan"
	if s := strings.Join(got.Strings(), ","); s != want {
		t.Errorf("Discover = %s, want %s", s, want)
	}
}

func newManager(t *testing.T, dir string, clock *fakeClock, src *sanSource) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{
		Dir:  dir,
		CA:   CAOptions{InstanceName: "Home", Hostname: "desk-pc"},
		SANs: src.get,
		Now:  clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func serial(m *Manager) string { return m.Current().Leaf.SerialNumber.String() }

func TestKeyFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits do not apply; Windows relies on the data directory's protected ACL")
	}
	dir := t.TempDir()
	src := &sanSource{in: SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.2")}, Hostname: "desk-pc", MDNSName: "bubbles"}}
	newManager(t, dir, &fakeClock{now: t0}, src)
	for _, name := range []string{CAKeyFile, LeafKeyFile} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, got)
		}
	}
}

func TestManagerPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: t0}
	src := &sanSource{in: SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.2")}, Hostname: "desk-pc", MDNSName: "bubbles"}}
	m1 := newManager(t, dir, clock, src)
	if m1.Mode() != ModeLocalCA || m1.CA() == nil {
		t.Fatalf("mode = %s", m1.Mode())
	}
	for _, f := range []string{CACertFile, CAKeyFile, LeafCertFile, LeafKeyFile} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	m2 := newManager(t, dir, clock, src)
	if m2.CA().Fingerprint() != m1.CA().Fingerprint() {
		t.Error("second start created a new CA")
	}
	if serial(m2) != serial(m1) {
		t.Error("second start re-issued an unchanged leaf")
	}
	got, err := m2.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil || got != m2.Current() {
		t.Errorf("GetCertificate = %v, %v", got, err)
	}
	st := m2.Status()
	if st.Mode != ModeLocalCA || st.CAFingerprint != m1.CA().Fingerprint() || !st.LeafNotAfter.Equal(m1.Current().Leaf.NotAfter) || len(st.SANs) == 0 {
		t.Errorf("status = %+v", st)
	}
}

func TestManagerReissuesOnSANChange(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: t0}
	src := &sanSource{in: SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.2")}, MDNSName: "bubbles"}}
	m := newManager(t, dir, clock, src)
	before := serial(m)

	// Same set in a different order: no re-issue.
	src.set(SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.2")}, MDNSName: "bubbles"})
	if changed, err := m.Check(); err != nil || changed {
		t.Fatalf("Check with same SANs = %v, %v", changed, err)
	}

	src.set(SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.77")}, MDNSName: "bubbles"})
	changed, err := m.Check()
	if err != nil || !changed {
		t.Fatalf("Check after DHCP change = %v, %v", changed, err)
	}
	if serial(m) == before {
		t.Error("serial unchanged after re-issue")
	}
	if _, err := m.Current().Leaf.Verify(x509.VerifyOptions{DNSName: "192.168.1.77", Roots: pool(m.CA()), CurrentTime: t0}); err != nil {
		t.Errorf("new leaf does not verify for the new address: %v", err)
	}
	onDisk, err := LoadLeafCert(dir)
	if err != nil || onDisk.SerialNumber.String() != serial(m) {
		t.Errorf("re-issued leaf not persisted (%v)", err)
	}
}

func TestManagerRenewsBeforeExpiry(t *testing.T) {
	clock := &fakeClock{now: t0}
	src := &sanSource{in: SANInput{MDNSName: "bubbles"}}
	m := newManager(t, t.TempDir(), clock, src)
	before := serial(m)

	clock.Add(LeafValidity - RenewBefore - 2*time.Hour) // 30 days + 1h left
	if changed, err := m.Check(); err != nil || changed {
		t.Fatalf("Check with 30d+1h left = %v, %v", changed, err)
	}
	clock.Add(2 * time.Hour) // 29 days 23 h left
	if changed, err := m.Check(); err != nil || !changed {
		t.Fatalf("Check with <30d left = %v, %v", changed, err)
	}
	if serial(m) == before || !m.Current().Leaf.NotBefore.Equal(clock.Now().Add(-time.Hour)) {
		t.Errorf("leaf not renewed: NotBefore %v", m.Current().Leaf.NotBefore)
	}
}

func TestManagerReissueForcesNewLeaf(t *testing.T) {
	m := newManager(t, t.TempDir(), &fakeClock{now: t0}, &sanSource{in: SANInput{MDNSName: "bubbles"}})
	before := serial(m)
	if err := m.Reissue(); err != nil {
		t.Fatal(err)
	}
	if serial(m) == before {
		t.Error("Reissue kept the old leaf")
	}
}

func TestManagerReplacesLeafFromOtherCA(t *testing.T) {
	dir := t.TempDir()
	other := testCA(t)
	leaf, err := other.Issue(SANs{DNS: []string{"bubbles.local"}}, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveLeaf(dir, leaf); err != nil {
		t.Fatal(err)
	}
	m := newManager(t, dir, &fakeClock{now: t0}, &sanSource{in: SANInput{MDNSName: "bubbles"}})
	if m.Current().Leaf.CheckSignatureFrom(m.CA().Cert) != nil {
		t.Error("leaf from a foreign CA kept in service")
	}
}

func TestLoadCAErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadCA(dir); !errors.Is(err, ErrNoCA) {
		t.Errorf("empty dir: %v", err)
	}
	ca := testCA(t)
	if err := ca.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, CAKeyFile)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreateCA(dir, CAOptions{}); err == nil || errors.Is(err, ErrNoCA) {
		t.Errorf("certificate without key: err = %v, want a hard error", err)
	}

	// A key without its certificate (crash during Save) is replaced.
	dir2 := t.TempDir()
	if err := ca.Save(dir2); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir2, CACertFile)); err != nil {
		t.Fatal(err)
	}
	got, created, err := LoadOrCreateCA(dir2, CAOptions{})
	if err != nil || !created || got.Fingerprint() == ca.Fingerprint() {
		t.Errorf("key without certificate: created=%v err=%v", created, err)
	}
}

func writePair(t *testing.T, certPath, keyPath string, leaf *tls.Certificate, mtime time.Time) {
	t.Helper()
	keyDER, err := x509.MarshalPKCS8PrivateKey(leaf.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Certificate[0]}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certPath, keyPath} {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBYOReload(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	ca := testCA(t)
	first, err := ca.Issue(SANs{DNS: []string{"chat.lan"}}, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	writePair(t, certPath, keyPath, first, t0)

	tlsDir := filepath.Join(dir, "tls")
	m, err := NewManager(ManagerOptions{Dir: tlsDir, CertFile: certPath, KeyFile: keyPath, Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	if m.Mode() != ModeBYO || m.CA() != nil || m.Status().CAFingerprint != "" {
		t.Fatalf("mode = %s, CA = %v", m.Mode(), m.CA())
	}
	if _, err := os.Stat(tlsDir); !os.IsNotExist(err) {
		t.Errorf("BYO mode wrote into the tls dir (stat err = %v)", err)
	}
	if !errors.Is(m.Reissue(), ErrBYO) {
		t.Error("Reissue in BYO mode did not return ErrBYO")
	}
	if changed, err := m.Check(); err != nil || changed {
		t.Fatalf("Check without change = %v, %v", changed, err)
	}

	second, err := ca.Issue(SANs{DNS: []string{"chat2.lan"}}, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	writePair(t, certPath, keyPath, second, t0.Add(time.Minute))
	if changed, err := m.Check(); err != nil || !changed {
		t.Fatalf("Check after replace = %v, %v", changed, err)
	}
	if got := m.Current().Leaf.DNSNames; len(got) != 1 || got[0] != "chat2.lan" {
		t.Errorf("served names = %v", got)
	}

	// A half-written pair is rejected and the previous one stays in service.
	if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(keyPath, t0.Add(2*time.Minute), t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if changed, err := m.Check(); err == nil || changed {
		t.Errorf("Check with broken key = %v, %v", changed, err)
	}
	if got := m.Current().Leaf.DNSNames[0]; got != "chat2.lan" {
		t.Errorf("served %q after a failed reload", got)
	}
}

// plistData walks an XML plist and returns the decoded <data> payloads and
// the <string> values that follow each <key>.
func plistData(t *testing.T, doc []byte) (data [][]byte, strs map[string][]string) {
	t.Helper()
	strs = map[string][]string{}
	dec := xml.NewDecoder(strings.NewReader(string(doc)))
	dec.Strict = true
	var elem, lastKey string
	sawPlist := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("mobileconfig is not well-formed XML: %v", err)
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			elem = tok.Name.Local
			if elem == "plist" {
				sawPlist = true
			}
		case xml.EndElement:
			elem = ""
		case xml.CharData:
			text := strings.TrimSpace(string(tok))
			switch elem {
			case "key":
				lastKey = text
			case "string":
				strs[lastKey] = append(strs[lastKey], text)
			case "data":
				b, err := base64.StdEncoding.DecodeString(text)
				if err != nil {
					t.Fatalf("data is not base64: %v", err)
				}
				data = append(data, b)
			}
		}
	}
	if !sawPlist {
		t.Fatal("no <plist> element")
	}
	return data, strs
}

func TestMobileConfigCarriesCA(t *testing.T) {
	ca := testCA(t)
	doc := ca.MobileConfig()
	data, strs := plistData(t, doc)
	if len(data) != 1 || string(data[0]) != string(ca.DER()) {
		t.Fatalf("profile data does not equal the CA DER (%d payloads)", len(data))
	}
	types := strings.Join(strs["PayloadType"], ",")
	if types != "com.apple.security.root,Configuration" {
		t.Errorf("PayloadType = %s", types)
	}
	uuid := regexp.MustCompile(`^[0-9A-F]{8}-[0-9A-F]{4}-5[0-9A-F]{3}-[89AB][0-9A-F]{3}-[0-9A-F]{12}$`)
	for _, u := range strs["PayloadUUID"] {
		if !uuid.MatchString(u) {
			t.Errorf("PayloadUUID %q", u)
		}
	}
	if string(ca.MobileConfig()) != string(doc) {
		t.Error("profile is not deterministic")
	}
}
