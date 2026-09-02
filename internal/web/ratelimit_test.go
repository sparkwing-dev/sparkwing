package web

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
)

func TestRateLimitMiddleware_Returns429(t *testing.T) {
	hits := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	l := ratelimit.New(2, time.Minute)
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
	h := rateLimitMiddleware(ratelimit.New(2, time.Minute), nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	h := rateLimitMiddleware(ratelimit.New(2, time.Minute), trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	h := rateLimitMiddleware(ratelimit.New(1, time.Minute), trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
