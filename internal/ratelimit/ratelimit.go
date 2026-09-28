// Package ratelimit provides the token bucket and client-address
// resolution shared by every Sparkwing listener that throttles
// unauthenticated work.
package ratelimit

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// MaxKeys bounds how many buckets one Limiter tracks. Keys come from
// untrusted input, so a full map sheds its least recently used
// buckets rather than growing without limit or refusing new keys.
const MaxKeys = 50000

// perf: evicting this fraction in one pass amortizes the O(n) scan across the insertions that follow it.
const evictFraction = 16

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// Limiter is a set of token buckets keyed by an arbitrary string,
// each refilling to burst over window. It is safe for concurrent use.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	window  time.Duration
}

// New returns a Limiter whose buckets hold burst tokens and refill a
// full burst over window.
func New(burst int, window time.Duration) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		burst:   float64(burst),
		window:  window,
	}
}

// Allow consumes one token for key and reports whether it was
// available. A full key space evicts its least recently used buckets,
// so an unseen client is always served.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, now, true)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// AllowWithRetry consumes a token for key as [Limiter.Allow] does, and
// on refusal reports how long the caller must wait for one. A refused
// attempt still charges the bucket, down to a bounded debt, so a caller
// that keeps knocking while empty is told to wait longer each time. The
// wait it reports is the real refill time, never shorter.
func (l *Limiter) AllowWithRetry(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, now, true)
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// safety: an unbounded debt would let a brief flood name a wait no caller
	// would honor, so the backlog one key can build is capped at a full window.
	b.tokens = max(b.tokens-1, -l.burst)
	wait := time.Duration((1 - b.tokens) / l.burst * float64(l.window))
	return false, max(wait, 0)
}

// Peek reports whether key has a token without consuming one. An
// untracked key reports true; it has spent nothing yet.
func (l *Limiter) Peek(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, now, false)
	return b == nil || b.tokens >= 1
}

// Penalize charges key one token, floored at empty. Use it to make a
// failed attempt, rather than every attempt, count against a budget.
func (l *Limiter) Penalize(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, now, true)
	b.tokens = max(b.tokens-1, 0)
}

// GC drops buckets idle long enough to have refilled.
func (l *Limiter) GC(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)
}

// Len returns the number of tracked buckets.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *Limiter) refill(key string, now time.Time, create bool) *bucket {
	b, ok := l.buckets[key]
	if !ok {
		if !create {
			return nil
		}
		if len(l.buckets) >= MaxKeys {
			l.evictLocked(now)
		}
		b = &bucket{tokens: l.burst, lastRefill: now}
		l.buckets[key] = b
		return b
	}
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		b.tokens += float64(elapsed) / float64(l.window) * l.burst
		b.tokens = min(b.tokens, l.burst)
		b.lastRefill = now
	}
	return b
}

// safety: refusing a new key would let a key flood deny every unseen client, so a full map drops its coldest buckets.
func (l *Limiter) evictLocked(now time.Time) {
	l.gcLocked(now)
	if len(l.buckets) < MaxKeys {
		return
	}
	seen := make([]time.Time, 0, len(l.buckets))
	for _, b := range l.buckets {
		seen = append(seen, b.lastRefill)
	}
	slices.SortFunc(seen, func(a, b time.Time) int { return a.Compare(b) })
	cutoff := seen[len(seen)/evictFraction]
	for k, b := range l.buckets {
		if !b.lastRefill.After(cutoff) {
			delete(l.buckets, k)
		}
	}
}

func (l *Limiter) gcLocked(now time.Time) {
	// safety: two idle windows refill any bucket to burst, so dropping it forgets nothing a caller could still be owed.
	for k, b := range l.buckets {
		if now.Sub(b.lastRefill) > 2*l.window {
			delete(l.buckets, k)
		}
	}
}

// ProxyAuthHeader carries the secret a fronting proxy presents to
// vouch for the X-Real-IP and X-Forwarded-Proto it sets.
const ProxyAuthHeader = "X-Sparkwing-Proxy-Auth"

// ProxyAuth recognizes requests relayed by a proxy that holds the
// shared secret. A nil *ProxyAuth recognizes none, so every forwarded
// header is ignored.
type ProxyAuth struct{ secret []byte }

// LoadProxyAuth reads the shared proxy secret from path. An empty path
// returns nil; a file with no secret in it is an error.
func LoadProxyAuth(path string) (*ProxyAuth, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := NewProxyAuth(string(raw))
	if p == nil {
		return nil, fmt.Errorf("%s holds no secret", path)
	}
	return p, nil
}

// NewProxyAuth recognizes requests carrying secret, ignoring surrounding
// whitespace. A blank secret returns nil.
func NewProxyAuth(secret string) *ProxyAuth {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	return &ProxyAuth{secret: []byte(secret)}
}

// Relayed reports whether r carries exactly one copy of the secret.
func (p *ProxyAuth) Relayed(r *http.Request) bool {
	if p == nil {
		return false
	}
	got := r.Header.Values(ProxyAuthHeader)
	return len(got) == 1 && subtle.ConstantTimeCompare([]byte(got[0]), p.secret) == 1
}

// Relay stamps an outbound request to the controller with the client
// address this process verified, and the secret that vouches for it.
// A nil *ProxyAuth or an empty address strips both headers instead, so
// a caller's copy never travels onward.
func (p *ProxyAuth) Relay(h http.Header, clientIP string) {
	if p == nil || clientIP == "" {
		h.Del(ProxyAuthHeader)
		h.Del("X-Real-IP")
		return
	}
	h.Set(ProxyAuthHeader, string(p.secret))
	h.Set("X-Real-IP", clientIP)
}

// ClientIP returns the address a limiter, an audit record or a relay
// should attribute r to: the single X-Real-IP of a request p
// recognizes as relayed, and the TCP peer otherwise.
func ClientIP(r *http.Request, p *ProxyAuth) string {
	peer, ok := remoteIP(r.RemoteAddr)
	if !ok {
		return r.RemoteAddr
	}
	if forwarded := r.Header.Values("X-Real-IP"); len(forwarded) == 1 && p.Relayed(r) {
		if ip, err := netip.ParseAddr(strings.TrimSpace(forwarded[0])); err == nil && ip.Zone() == "" {
			return ip.Unmap().String()
		}
	}
	return peer.String()
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
