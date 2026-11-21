package tlsca

import (
	"crypto/tls"
	"net/netip"
	"path/filepath"
	"testing"
	"time"
)

// TestSNICertServedByName: the by-name certificate (Tailscale's) answers
// handshakes for its name only, while valid, and is reloaded with Check.
func TestSNICertServedByName(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{now: t0}
	src := &sanSource{}
	src.set(SANInput{Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.20")}, Hostname: "desk-pc"})
	m := newManager(t, dir, clock, src)
	main := m.Current()

	// Stand-in for the Tailscale certificate: any pair for the name.
	other := testCA(t, "tail1234.ts.net")
	first, err := other.Issue(SANs{DNS: []string{"chat-pi.tail1234.ts.net"}}, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "ts.crt"), filepath.Join(dir, "ts.key")
	writePair(t, certPath, keyPath, first, t0)

	if err := m.SetSNICert([]string{"Chat-Pi.tail1234.ts.net."}, filepath.Join(dir, "missing.crt"), keyPath); err == nil {
		t.Fatal("missing files accepted")
	}
	if m.Status().SNI != nil {
		t.Fatal("a failed SetSNICert left a certificate")
	}
	if err := m.SetSNICert([]string{"Chat-Pi.tail1234.ts.net."}, certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	get := func(name string) *tls.Certificate {
		t.Helper()
		c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := get("chat-pi.tail1234.ts.net"); c.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatal("the tailnet name did not get its certificate")
	}
	for _, name := range []string{"", "desk-pc", "bubbles.local", "other.tail1234.ts.net"} {
		if get(name) != main {
			t.Errorf("%q did not get the main certificate", name)
		}
	}
	if c, _ := m.GetCertificate(nil); c != main {
		t.Error("nil hello did not get the main certificate")
	}
	st := m.Status().SNI
	if st == nil || len(st.Names) != 1 || st.Names[0] != "chat-pi.tail1234.ts.net" || st.Fingerprint == "" {
		t.Fatalf("status %+v", st)
	}

	// Renewed files are picked up by Check.
	second, err := other.Issue(SANs{DNS: []string{"chat-pi.tail1234.ts.net"}}, t0, nil)
	if err != nil {
		t.Fatal(err)
	}
	writePair(t, certPath, keyPath, second, t0.Add(time.Minute))
	if changed, err := m.Check(); err != nil || !changed {
		t.Fatalf("Check after renewal = %v, %v", changed, err)
	}
	if c := get("chat-pi.tail1234.ts.net"); c.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Fatal("renewed certificate not served")
	}

	// Expired: the main certificate answers instead.
	clock.Add(second.Leaf.NotAfter.Sub(t0) + time.Hour)
	if get("chat-pi.tail1234.ts.net").Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) == 0 {
		t.Fatal("expired by-name certificate served")
	}

	m.ClearSNICert()
	if m.Status().SNI != nil {
		t.Fatal("ClearSNICert kept it")
	}
}
