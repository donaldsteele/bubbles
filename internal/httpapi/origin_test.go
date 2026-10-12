package httpapi

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSameOrigin(t *testing.T) {
	const host = "bubbles.lan:28225"
	tests := []struct {
		name    string
		tls     bool
		origin  string
		referer string
		want    bool
	}{
		{"https origin matches over TLS", true, "https://bubbles.lan:28225", "", true},
		{"host comparison ignores case", true, "https://BUBBLES.lan:28225", "", true},
		{"http origin refused over TLS", true, "http://bubbles.lan:28225", "", false},
		{"other host refused over TLS", true, "https://evil.example:28225", "", false},
		{"other port refused over TLS", true, "https://bubbles.lan:9443", "", false},
		{"missing port refused over TLS", true, "https://bubbles.lan", "", false},
		{"null origin refused over TLS", true, "null", "", false},
		{"https referer used when no origin", true, "", "https://bubbles.lan:28225/settings", true},
		{"http referer refused over TLS", true, "", "http://bubbles.lan:28225/settings", false},
		{"no origin or referer refused", true, "", "", false},
		{"http origin accepted without TLS", false, "http://bubbles.lan:28225", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := "http://"
			if tt.tls {
				scheme = "https://"
			}
			r := httptest.NewRequest(http.MethodPost, scheme+host+"/api/v1/auth/login", nil)
			r.Host = host
			r.TLS = nil
			if tt.tls {
				r.TLS = &tls.ConnectionState{ServerName: "bubbles.lan"}
			}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				r.Header.Set("Referer", tt.referer)
			}
			if got := SameOrigin(r); got != tt.want {
				t.Errorf("SameOrigin = %v, want %v", got, tt.want)
			}
		})
	}
}
