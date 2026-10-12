package discovery

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brutella/dnssd"
)

func TestFingerprint16(t *testing.T) {
	if got := Fingerprint16(nil); got != "" {
		t.Fatalf("empty der = %q", got)
	}
	// sha256("abc") = ba7816bf8f01cfea...
	if got, want := Fingerprint16([]byte("abc")), "ba7816bf8f01cfea"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTXT(t *testing.T) {
	tests := []struct {
		name string
		fp   string
		port int
		want map[string]string
	}{
		{"normal", "ba7816bf8f01cfea", 28226, map[string]string{"v": "1", "fp": "ba7816bf8f01cfea", "onboard": "28226", "path": "/"}},
		{"byo", "", 80, map[string]string{"v": "1", "fp": "", "onboard": "80", "path": "/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TXT(tc.fp, tc.port)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v", got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("%s = %q want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestStartRejectsNoPort(t *testing.T) {
	a := New(Options{Instance: "x", Host: "bubbles"})
	if err := a.Start(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	a.Refresh()
	a.Close()
}

func TestLiveAdvertise(t *testing.T) {
	if os.Getenv("BUBBLES_LIVE_MDNS") != "1" {
		t.Skip("set BUBBLES_LIVE_MDNS=1")
	}
	a := New(Options{
		Instance: "bubbles-live-test", Host: "bubbles-live", HTTPSPort: 18443, HTTPPort: 18080,
		CADER: func() []byte { return []byte("abc") },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	found := make(chan dnssd.BrowseEntry, 1)
	bctx, bcancel := context.WithTimeout(ctx, 10*time.Second)
	defer bcancel()
	go func() {
		_ = dnssd.LookupType(bctx, ServiceType+".local.", func(e dnssd.BrowseEntry) {
			if strings.HasPrefix(e.Name, "bubbles-live-test") {
				select {
				case found <- e:
				default:
				}
			}
		}, func(dnssd.BrowseEntry) {})
	}()
	select {
	case e := <-found:
		if e.Text["fp"] != "ba7816bf8f01cfea" || e.Text["onboard"] != "18080" || e.Port != 18443 {
			t.Fatalf("bad entry %+v", e)
		}
	case <-bctx.Done():
		t.Fatal("service not discovered")
	}
}
