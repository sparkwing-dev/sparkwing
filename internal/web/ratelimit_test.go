package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestRateLimiter_BucketDrainAndRefill(t *testing.T) {
	l := newRateLimiter(3, time.Minute)
	now := time.Unix(0, 0)

	for i := range 3 {
		if !l.allow("1.2.3.4", now) {
			t.Fatalf("expected allow on attempt %d", i+1)
		}
	}
	if l.allow("1.2.3.4", now) {
		t.Fatalf("expected deny once bucket drains")
	}

	half := now.Add(30 * time.Second)
	if !l.allow("1.2.3.4", half) {
		t.Fatalf("expected allow after half-window refill")
	}
	if l.allow("1.2.3.4", half) {
		t.Fatalf("expected deny after the half-window allow consumed the refill")
	}
}

func TestRateLimiter_IsolatedPerKey(t *testing.T) {
	l := newRateLimiter(2, time.Minute)
	now := time.Unix(0, 0)
	for range 2 {
		_ = l.allow("attacker", now)
	}
	if l.allow("attacker", now) {
		t.Fatalf("attacker bucket should be drained")
	}
	if !l.allow("victim", now) {
		t.Fatalf("victim should be unaffected by attacker's drain")
	}
}

func TestRateLimitMiddleware_Returns429(t *testing.T) {
	hits := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	l := newRateLimiter(2, time.Minute)
	h := rateLimitMiddleware(l, nil, inner)

	send := func(method string) int {
		req := httptest.NewRequest(method, "/login", nil)
		req.RemoteAddr = "10.0.0.1:5000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := send(http.MethodPost); c != 200 {
		t.Fatalf("first POST: %d", c)
	}
	if c := send(http.MethodPost); c != 200 {
		t.Fatalf("second POST: %d", c)
	}
	if c := send(http.MethodPost); c != http.StatusTooManyRequests {
		t.Fatalf("third POST: got %d want 429", c)
	}
	for i := range 5 {
		if c := send(http.MethodGet); c != 200 {
			t.Fatalf("GET %d should pass: got %d", i, c)
		}
	}
}

func TestRateLimitMiddleware_DirectPeerCannotRotateWithForwardedHeader(t *testing.T) {
	hits := 0
	h := rateLimitMiddleware(newRateLimiter(2, time.Minute), nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	for attempt, forwarded := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "203.0.113.9:5000"
		req.Header.Set("X-Forwarded-For", forwarded)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := http.StatusOK
		if attempt == 2 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, rec.Code, want)
		}
	}
	if hits != 2 {
		t.Fatalf("handler hits = %d, want 2", hits)
	}
}

func TestRateLimitMiddleware_TrustedProxyIgnoresSpoofedLeftChain(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := rateLimitMiddleware(newRateLimiter(2, time.Minute), trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for attempt, spoofed := range []string{"unknown", "also-bad", "still-not-an-ip"} {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "10.0.0.5:5000"
		req.Header.Set("X-Forwarded-For", spoofed+", 203.0.113.9")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		want := http.StatusOK
		if attempt == 2 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, rec.Code, want)
		}
	}
}

func TestRateLimitMiddleware_MalformedLeftPrefixDoesNotCollapseClients(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	h := rateLimitMiddleware(newRateLimiter(1, time.Minute), trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	send := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "10.0.0.5:5000"
		req.Header.Set("X-Forwarded-For", "unknown, "+client)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for attempt, tc := range []struct {
		client string
		want   int
	}{
		{client: "203.0.113.9", want: http.StatusOK},
		{client: "203.0.113.10", want: http.StatusOK},
		{client: "203.0.113.9", want: http.StatusTooManyRequests},
	} {
		if got := send(tc.client); got != tc.want {
			t.Fatalf("attempt %d status = %d, want %d", attempt+1, got, tc.want)
		}
	}
}

func TestClientIP_TrustedProxyChain(t *testing.T) {
	trustedEdge := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	trustedIPv6Edge := []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/48")}
	trustedChain := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	cases := []struct {
		name       string
		xff        []string
		remoteAddr string
		trusted    []netip.Prefix
		want       string
	}{
		{name: "default ignores forwarded header", xff: []string{"198.51.100.7"}, remoteAddr: "203.0.113.9:5000", want: "203.0.113.9"},
		{name: "trusted edge accepts client", xff: []string{"198.51.100.7"}, remoteAddr: "10.0.0.1:5000", trusted: trustedEdge, want: "198.51.100.7"},
		{name: "trusted IPv6 edge accepts client", xff: []string{"2001:db8:ffff::7"}, remoteAddr: "[2001:db8:1::5]:5000", trusted: trustedIPv6Edge, want: "2001:db8:ffff::7"},
		{name: "append chain stops at nearest untrusted hop", xff: []string{"198.51.100.99, 203.0.113.9"}, remoteAddr: "10.0.0.1:5000", trusted: trustedEdge, want: "203.0.113.9"},
		{name: "malformed left prefix after client is ignored", xff: []string{"unknown, 203.0.113.9"}, remoteAddr: "10.0.0.1:5000", trusted: trustedEdge, want: "203.0.113.9"},
		{name: "multiple trusted hops", xff: []string{"198.51.100.7, 192.168.1.5"}, remoteAddr: "10.0.0.1:5000", trusted: trustedChain, want: "198.51.100.7"},
		{name: "multiple header fields", xff: []string{"198.51.100.7", "192.168.1.5"}, remoteAddr: "10.0.0.1:5000", trusted: trustedChain, want: "198.51.100.7"},
		{name: "malformed chain falls back to peer", xff: []string{"198.51.100.7, unknown"}, remoteAddr: "10.0.0.1:5000", trusted: trustedEdge, want: "10.0.0.1"},
		{name: "untrusted peer ignores valid chain", xff: []string{"198.51.100.7"}, remoteAddr: "203.0.113.9:5000", trusted: trustedEdge, want: "203.0.113.9"},
		{name: "missing forwarded header uses peer", remoteAddr: "10.0.0.1:5000", trusted: trustedEdge, want: "10.0.0.1"},
		{name: "peer without port", remoteAddr: "127.0.0.1", want: "127.0.0.1"},
		{name: "malformed peer stays opaque", remoteAddr: "local-peer", trusted: trustedEdge, want: "local-peer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = tc.remoteAddr
			for _, value := range tc.xff {
				req.Header.Add("X-Forwarded-For", value)
			}
			if got := clientIP(req, tc.trusted); got != tc.want {
				t.Fatalf("clientIP=%q want %q", got, tc.want)
			}
		})
	}
}
