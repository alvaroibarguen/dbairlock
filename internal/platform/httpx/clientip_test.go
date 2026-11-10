package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxy := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	cases := []struct {
		name    string
		remote  string
		xff     string
		trusted []netip.Prefix
		want    string
	}{
		{"direct", "203.0.113.7:5000", "", nil, "203.0.113.7"},
		{"untrusted peer ignores the header", "203.0.113.7:5000", "198.51.100.1", proxy, "203.0.113.7"},
		{"trusted proxy", "10.1.2.3:5000", "198.51.100.1", proxy, "198.51.100.1"},
		{"forged hop before the real client", "10.1.2.3:5000", "1.1.1.1, 198.51.100.1", proxy, "198.51.100.1"},
		{"chain of trusted proxies", "10.1.2.3:5000", "198.51.100.1, 10.9.9.9", proxy, "198.51.100.1"},
		{"no header", "10.1.2.3:5000", "", proxy, "10.1.2.3"},
		{"garbage", "10.1.2.3:5000", "not-an-ip", proxy, "10.1.2.3"},
		{"ipv6", "[2001:db8::1]:443", "", nil, "2001:db8::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			h := ClientIP(func() []netip.Prefix { return tc.trusted })(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = ClientIPFrom(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
