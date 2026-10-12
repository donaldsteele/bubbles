package netutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestIsPrivate(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"172.15.255.255", false},
		{"172.32.0.1", false},
		{"192.168.1.5", true},
		{"192.169.0.1", false},
		{"169.254.10.20", true},
		{"100.64.0.1", true},
		{"100.100.100.100", true},
		{"100.127.255.255", true},
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		{"127.0.0.1", true},
		{"127.255.0.9", true},
		{"::1", true},
		{"fc00::1", true},
		{"fd12:3456:789a::1", true},
		{"fe80::1", true},
		{"fe80::1%eth0", true},
		{"febf::1", true},
		{"fec0::1", false},
		{"::ffff:192.168.1.5", true},
		{"::ffff:8.8.8.8", false},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"203.0.113.9", false},
		{"2001:4860:4860::8888", false},
		{"2606:4700::1111", false},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			addr, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tt.addr, err)
			}
			if got := IsPrivate(addr); got != tt.want {
				t.Errorf("IsPrivate(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
	if IsPrivate(netip.Addr{}) {
		t.Error("IsPrivate(zero Addr) = true, want false")
	}
}

func TestIsVirtualName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"vEthernet (Default Switch)", true},
		{"vethernet (wsl)", true},
		{"VMware Network Adapter VMnet8", true},
		{"VirtualBox Host-Only Network", true},
		{"docker0", true},
		{"docker_gwbridge", true},
		{"br-1a2b3c4d", true},
		{"veth1234ab", true},
		{"utun3", true},
		{"awdl0", true},
		{"llw0", true},
		{"bridge100", true},
		{"Ethernet", false},
		{"Wi-Fi", false},
		{"en0", false},
		{"eth0", false},
		{"wlan0", false},
		{"Tailscale", false},
		{"tailscale0", false},
		{"enp3s0", false},
	}
	for _, tt := range tests {
		if got := IsVirtualName(tt.name); got != tt.want {
			t.Errorf("IsVirtualName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func addrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		a, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("ParseAddr(%q): %v", s, err)
		}
		out = append(out, a)
	}
	return out
}

func TestRankExcludesVirtualAndUnusable(t *testing.T) {
	ifaces := []Interface{
		{Name: "lo", Index: 1, Up: true, Loopback: true, Addrs: addrs(t, "127.0.0.1", "::1")},
		{Name: "vEthernet (Default Switch)", Index: 2, Up: true, Addrs: addrs(t, "172.28.0.1")},
		{Name: "VMware Network Adapter VMnet1", Index: 3, Up: true, Addrs: addrs(t, "192.168.100.1")},
		{Name: "VirtualBox Host-Only Network", Index: 4, Up: true, Addrs: addrs(t, "192.168.56.1")},
		{Name: "docker0", Index: 5, Up: true, Addrs: addrs(t, "172.17.0.1")},
		{Name: "br-abc123", Index: 6, Up: true, Addrs: addrs(t, "172.18.0.1")},
		{Name: "veth9f", Index: 7, Up: true, Addrs: addrs(t, "fe80::1")},
		{Name: "utun2", Index: 8, Up: true, Addrs: addrs(t, "100.64.1.1")},
		{Name: "awdl0", Index: 9, Up: true, Addrs: addrs(t, "fe80::2")},
		{Name: "llw0", Index: 10, Up: true, Addrs: addrs(t, "fe80::3")},
		{Name: "bridge0", Index: 11, Up: true, Addrs: addrs(t, "192.168.64.1")},
		{Name: "Ethernet 2", Index: 12, Up: false, Addrs: addrs(t, "192.168.7.7")},
		{Name: "Public", Index: 13, Up: true, Addrs: addrs(t, "203.0.113.7", "2001:db8::7")},
		{Name: "Ethernet", Index: 14, Up: true, Addrs: addrs(t, "192.168.1.20", "0.0.0.0", "224.0.0.251")},
	}
	got := Addrs(Rank(ifaces))
	want := addrs(t, "192.168.1.20")
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("Rank addresses = %v, want %v", got, want)
	}
}

func TestRankOrderAndTailscale(t *testing.T) {
	ifaces := []Interface{
		{Name: "eth1", Index: 9, Up: true, Addrs: addrs(t, "fe80::9", "169.254.3.4")},
		{Name: "Tailscale", Index: 8, Up: true, Addrs: addrs(t, "100.101.102.103", "fd7a:115c:a1e0::1")},
		{Name: "eth0", Index: 2, Up: true, Addrs: addrs(t, "10.0.0.5", "192.168.1.5", "172.20.0.5", "::ffff:192.168.1.6")},
		{Name: "wlan0", Index: 3, Up: true, Addrs: addrs(t, "192.168.1.5", "192.168.0.9")},
	}
	got := Rank(ifaces)
	// 192.168/16 first (by interface index, then address), then 10/8, 172.16/12,
	// CGNAT (Tailscale is allowed), ULA, IPv4 link-local, IPv6 link-local.
	want := addrs(t,
		"192.168.1.5", "192.168.1.6", "192.168.0.9",
		"10.0.0.5",
		"172.20.0.5",
		"100.101.102.103",
		"fd7a:115c:a1e0::1",
		"169.254.3.4",
		"fe80::9",
	)
	gotAddrs := Addrs(got)
	if len(gotAddrs) != len(want) {
		t.Fatalf("Rank addresses = %v, want %v", gotAddrs, want)
	}
	for i := range want {
		if gotAddrs[i] != want[i] {
			t.Fatalf("Rank addresses = %v, want %v", gotAddrs, want)
		}
	}
	if got[0].Interface != "eth0" {
		t.Errorf("first candidate interface = %q, want eth0 (duplicate keeps best occurrence)", got[0].Interface)
	}
}

func TestRankEmpty(t *testing.T) {
	if got := Rank(nil); len(got) != 0 {
		t.Fatalf("Rank(nil) = %v, want empty", got)
	}
}

func TestCandidatesInvariants(t *testing.T) {
	cands, err := Candidates()
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	for _, c := range cands {
		if !IsPrivate(c.Addr) || c.Addr.IsLoopback() || IsVirtualName(c.Interface) {
			t.Errorf("unexpected candidate %+v", c)
		}
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func guardRequest(t *testing.T, h http.Handler, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGuard(t *testing.T) {
	allowed := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	h := Guard(allowed)(okHandler())
	tests := []struct {
		remote string
		want   int
	}{
		{"192.168.1.5:1234", http.StatusNoContent},
		{"10.1.2.3:80", http.StatusNoContent},
		{"100.64.0.9:443", http.StatusNoContent},
		{"127.0.0.1:5555", http.StatusNoContent},
		{"[::1]:5555", http.StatusNoContent},
		{"[fd00::5]:5555", http.StatusNoContent},
		{"[::ffff:192.168.1.5]:1234", http.StatusNoContent},
		{"8.8.8.8:1234", http.StatusForbidden},
		{"[2606:4700::1111]:443", http.StatusForbidden},
		{"[::ffff:8.8.8.8]:1234", http.StatusForbidden},
		{"203.0.113.77:1234", http.StatusNoContent},
		{"[2001:db8::1]:1234", http.StatusNoContent},
		{"203.0.114.1:1234", http.StatusForbidden},
		{"", http.StatusForbidden},
		{"not-an-address", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			rec := guardRequest(t, h, tt.remote)
			if rec.Code != tt.want {
				t.Errorf("RemoteAddr %q: status = %d, want %d", tt.remote, rec.Code, tt.want)
			}
		})
	}
}

func TestGuardForbiddenBody(t *testing.T) {
	rec := guardRequest(t, Guard(nil)(okHandler()), "8.8.8.8:1234")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Error.Code != "forbidden" || body.Error.Message == "" {
		t.Errorf("body = %+v, want code forbidden and a message", body)
	}
}

func TestGuardFuncReadsAllowListPerRequest(t *testing.T) {
	var list []netip.Prefix
	h := GuardFunc(func() []netip.Prefix { return list })(okHandler())
	if rec := guardRequest(t, h, "8.8.8.8:1"); rec.Code != http.StatusForbidden {
		t.Fatalf("before allow: status = %d, want 403", rec.Code)
	}
	list = []netip.Prefix{netip.MustParsePrefix("8.8.8.0/24")}
	if rec := guardRequest(t, h, "8.8.8.8:1"); rec.Code != http.StatusNoContent {
		t.Fatalf("after allow: status = %d, want 204", rec.Code)
	}
}
