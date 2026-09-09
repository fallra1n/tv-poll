package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name              string
		remoteAddr        string
		forwardedFor      string
		realIP            string
		trustProxyHeaders bool
		want              string
	}{
		{
			name:              "ignores untrusted forwarded header",
			remoteAddr:        "203.0.113.10:4321",
			forwardedFor:      "198.51.100.20",
			trustProxyHeaders: false,
			want:              "203.0.113.10",
		},
		{
			name:              "uses first forwarded address",
			remoteAddr:        "172.18.0.1:4321",
			forwardedFor:      "198.51.100.20, 203.0.113.10",
			trustProxyHeaders: true,
			want:              "198.51.100.20",
		},
		{
			name:              "supports forwarded IPv6",
			remoteAddr:        "172.18.0.1:4321",
			forwardedFor:      "2001:db8::1",
			trustProxyHeaders: true,
			want:              "2001:db8::1",
		},
		{
			name:              "falls back to real IP",
			remoteAddr:        "172.18.0.1:4321",
			forwardedFor:      "not-an-ip",
			realIP:            "198.51.100.30",
			trustProxyHeaders: true,
			want:              "198.51.100.30",
		},
		{
			name:              "falls back to remote address",
			remoteAddr:        "172.18.0.1:4321",
			forwardedFor:      "not-an-ip",
			realIP:            "also-not-an-ip",
			trustProxyHeaders: true,
			want:              "172.18.0.1",
		},
		{
			name:              "keeps unparseable remote address",
			remoteAddr:        "local-transport",
			trustProxyHeaders: false,
			want:              "local-transport",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://example.test/", nil)
			r.RemoteAddr = tt.remoteAddr
			r.Header.Set("X-Forwarded-For", tt.forwardedFor)
			r.Header.Set("X-Real-IP", tt.realIP)

			if got := clientIP(r, tt.trustProxyHeaders); got != tt.want {
				t.Fatalf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
