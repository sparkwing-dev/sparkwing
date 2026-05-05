package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Per-IP cap for POST /login and POST /login/bootstrap: generous for
// humans, painful for credential-stuffing scripts.
const (
	loginRateBurst  = 10
	loginRateWindow = 60 * time.Second
)

type rateBucket struct {
	tokens     float64
	lastRefill time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	burst   float64
	window  time.Duration
}

func newRateLimiter(burst int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*rateBucket),
		burst:   float64(burst),
		window:  window,
	}
}

// allow reports whether a request from key should be served.
func (l *rateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		b = &rateBucket{tokens: l.burst, lastRefill: now}
		l.buckets[key] = b
	}
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		b.tokens += float64(elapsed) / float64(l.window) * l.burst
		b.tokens = min(b.tokens, l.burst)
		b.lastRefill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gc drops fully-refilled buckets untouched for window*2. Bounded
// growth without a per-request scan; a missing call lets the map grow
// but is safe.
func (l *rateLimiter) gc(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if now.Sub(b.lastRefill) > 2*l.window && b.tokens >= l.burst {
			delete(l.buckets, k)
		}
	}
}

// rateLimitMiddleware wraps a handler with per-IP token-bucket
// limiting. GETs bypass since only POSTs are brute-forceable.
func rateLimitMiddleware(l *rateLimiter, next http.Handler) http.Handler {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			l.gc(time.Now())
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			http.Error(w,
				"too many login attempts; try again in a minute",
				http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP picks the best-effort source IP for rate-limit keying.
// Honors X-Forwarded-For first-hop (nginx in prod), falls back to
// RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
