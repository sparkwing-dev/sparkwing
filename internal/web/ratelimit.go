package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

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

func (l *rateLimiter) gc(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if now.Sub(b.lastRefill) > 2*l.window && b.tokens >= l.burst {
			delete(l.buckets, k)
		}
	}
}

func rateLimitMiddleware(l *rateLimiter, trustedProxyCIDRs []netip.Prefix, next http.Handler) http.Handler {
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
		if !l.allow(clientIP(r, trustedProxyCIDRs), time.Now()) {
			w.Header().Set("Retry-After", "60")
			http.Error(w,
				"too many login attempts; try again in a minute",
				http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request, trustedProxyCIDRs []netip.Prefix) string {
	peer, ok := remoteIP(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if !isTrustedProxy(peer, trustedProxyCIDRs) {
		return peer.String()
	}
	client := peer
	values := r.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		remaining := values[i]
		for {
			comma := strings.LastIndexByte(remaining, ',')
			raw := remaining
			if comma >= 0 {
				raw = remaining[comma+1:]
				remaining = remaining[:comma]
			}
			ip, err := netip.ParseAddr(strings.TrimSpace(raw))
			if err != nil || ip.Zone() != "" {
				return peer.String()
			}
			client = ip.Unmap()
			if !isTrustedProxy(client, trustedProxyCIDRs) {
				return client.String()
			}
			if comma < 0 {
				break
			}
		}
	}
	return client.String()
}

func remoteIP(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil || ip.Zone() != "" {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func isTrustedProxy(ip netip.Addr, trustedProxyCIDRs []netip.Prefix) bool {
	for _, prefix := range trustedProxyCIDRs {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
