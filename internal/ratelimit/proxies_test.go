package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"testing/quick"
)

func TestTrustedProxyClientAddress(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8:ffff::/48")}
	for _, tc := range []struct{ peer, forwarded, want string }{
		{"10.0.0.2:443", "198.51.100.7", "198.51.100.7"},
		{"10.0.0.2:443", "192.0.2.1, 198.51.100.7, 10.0.0.1", "198.51.100.7"},
		{"203.0.113.5:443", "198.51.100.7", "203.0.113.5"},
		{"10.0.0.2:443", "198.51.100.7, invalid", "10.0.0.2"},
		{"10.0.0.2:443", "", "10.0.0.2"},
		{"[2001:db8:ffff::2]:443", "2001:db8:1::7, 2001:db8:ffff::1", "2001:db8:1::7"},
		{"[::ffff:10.0.0.2]:443", "::ffff:198.51.100.7", "198.51.100.7"},
	} {
		t.Run(tc.peer+"/"+tc.forwarded, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.peer
			req.Header.Set("X-Forwarded-For", tc.forwarded)
			var got string
			TrustedProxies(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) }), prefixes).ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("client = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUntrustedForwardedPrefixCannotChangeClient(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	property := func(spoof string, last uint8) bool {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.2:443"
		client := netip.AddrFrom4([4]byte{198, 51, 100, last}).String()
		req.Header.Set("X-Forwarded-For", spoof+", "+client+", 10.0.0.1")
		var got string
		TrustedProxies(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) }), prefixes).ServeHTTP(httptest.NewRecorder(), req)
		return got == client
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func TestUntrustedPeerCannotAssertForwardedClient(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	property := func(forwarded string, last uint8) bool {
		peer := netip.AddrFrom4([4]byte{203, 0, 113, last}).String()
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = peer + ":5000"
		req.Header.Set("X-Forwarded-For", forwarded)
		req.Header.Set("X-Real-IP", forwarded)
		var got string
		TrustedProxies(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) }), prefixes).ServeHTTP(httptest.NewRecorder(), req)
		return got == peer
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}
