// Package ratelimit provides the token bucket and client-address
// resolution shared by every Sparkwing listener that throttles
// unauthenticated work.
package ratelimit

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
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

// ClientIP returns the address a limiter should key on: the TCP peer,
// or the nearest untrusted address in an append-style X-Forwarded-For
// chain when the peer is itself a trusted proxy.
func ClientIP(r *http.Request, trustedProxyCIDRs []netip.Prefix) string {
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

// ParseTrustedProxyCIDRs parses a comma-separated CIDR list into
// masked prefixes. IPv4-mapped prefixes normalize to IPv4; an empty
// string yields no prefixes, which makes every peer untrusted.
func ParseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", part, err)
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("IPv4-mapped CIDR %q must use prefix length /96 through /128", part)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
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
