package tailnet

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

const runningJSON = `{
  "Version": "1.90.0",
  "BackendState": "Running",
  "Self": {
    "HostName": "chat-pi",
    "DNSName": "Chat-Pi.tail1234.ts.net.",
    "TailscaleIPs": ["100.101.102.103", "fd7a:115c:a1e0::1"]
  },
  "MagicDNSSuffix": "tail1234.ts.net",
  "CertDomains": ["chat-pi.tail1234.ts.net"]
}`

func TestParseStatus(t *testing.T) {
	info, err := ParseStatus([]byte(runningJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("100.101.102.103"), netip.MustParseAddr("fd7a:115c:a1e0::1")}
	if !info.Running || info.DNSName != "chat-pi.tail1234.ts.net" || !slices.Equal(info.IPs, want) || !info.CanCert() {
		t.Fatalf("info %+v", info)
	}

	for name, js := range map[string]string{
		"stopped":    `{"BackendState":"Stopped","Self":{"DNSName":"chat-pi.tail1234.ts.net."}}`,
		"logged out": `{"BackendState":"NeedsLogin"}`,
		"no self":    `{"BackendState":"Running"}`,
	} {
		info, err := ParseStatus([]byte(js))
		if err != nil || info.Running {
			t.Errorf("%s: %+v %v", name, info, err)
		}
	}

	// MagicDNS off: no name; HTTPS off: no certificate.
	info, err = ParseStatus([]byte(`{"BackendState":"Running","Self":{"DNSName":"","TailscaleIPs":["100.64.0.9"]}}`))
	if err != nil || !info.Running || info.DNSName != "" || info.CanCert() || len(info.IPs) != 1 {
		t.Fatalf("no MagicDNS: %+v %v", info, err)
	}
	info, _ = ParseStatus([]byte(`{"BackendState":"Running","Self":{"DNSName":"a.tail1.ts.net."}}`))
	if info.DNSName != "a.tail1.ts.net" || info.CanCert() {
		t.Fatalf("no cert domains: %+v", info)
	}
	if _, err := ParseStatus([]byte("not json")); err == nil {
		t.Fatal("garbage parsed")
	}
}

func TestStatusNotInstalledAndCache(t *testing.T) {
	calls := 0
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	c := &Client{
		Now: func() time.Time { return now },
		Run: func(context.Context, ...string) ([]byte, error) { calls++; return nil, ErrNoTailscale },
	}
	info, err := c.Status(context.Background())
	if err != nil || info.Running {
		t.Fatalf("not installed: %+v %v", info, err)
	}
	c.Status(context.Background())
	if calls != 1 {
		t.Fatalf("cached answer not reused: %d calls", calls)
	}
	now = now.Add(2 * time.Minute)
	c.Status(context.Background())
	if calls != 2 {
		t.Fatalf("stale answer reused: %d calls", calls)
	}
	c.Forget()
	c.Status(context.Background())
	if calls != 3 {
		t.Fatalf("Forget kept the answer: %d calls", calls)
	}

	// Installed but the daemon is down: the command fails with no output.
	down := &Client{Run: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }}
	if info, err := down.Status(context.Background()); err != nil || info.Running {
		t.Fatalf("daemon down: %+v %v", info, err)
	}
}

func TestCertArguments(t *testing.T) {
	var got []string
	c := &Client{Run: func(_ context.Context, args ...string) ([]byte, error) { got = args; return nil, nil }}
	dir := t.TempDir()
	if err := c.Cert(context.Background(), "chat-pi.tail1234.ts.net", dir+"/ts/c.crt", dir+"/ts/c.key"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "cert --cert-file "+dir+"/ts/c.crt --key-file "+dir+"/ts/c.key chat-pi.tail1234.ts.net" {
		t.Fatalf("args %q", got)
	}
	if err := c.Cert(context.Background(), "-oops", dir+"/c", dir+"/k"); err == nil {
		t.Fatal("a flag-like name was passed to tailscale")
	}
}
