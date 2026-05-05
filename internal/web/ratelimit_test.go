package web

import (
	"net/http"
	"net/http/httptest"
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

	// Half a window later: 1.5 tokens refilled, so one more allow
	// fits before we run out again.
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
	h := rateLimitMiddleware(l, inner)

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

func TestClientIP_XForwardedFor(t *testing.T) {
	cases := []struct {
		name       string
		xff        string
		remoteAddr string
		want       string
	}{
		{"xff single", "203.0.113.5", "10.0.0.1:5000", "203.0.113.5"},
		{"xff chain", "203.0.113.5, 10.0.0.99", "10.0.0.1:5000", "203.0.113.5"},
		{"xff with spaces", "  203.0.113.5  ", "10.0.0.1:5000", "203.0.113.5"},
		{"no xff", "", "10.0.0.1:5000", "10.0.0.1"},
		{"no port in remoteaddr", "", "127.0.0.1", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := clientIP(req); got != tc.want {
				t.Fatalf("clientIP=%q want %q", got, tc.want)
			}
		})
	}
}
