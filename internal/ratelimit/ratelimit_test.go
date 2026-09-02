package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestLimiter_BucketDrainAndRefill(t *testing.T) {
	l := New(3, time.Minute)
	now := time.Unix(0, 0)

	for i := range 3 {
		if !l.Allow("1.2.3.4", now) {
			t.Fatalf("expected allow on attempt %d", i+1)
		}
	}
	if l.Allow("1.2.3.4", now) {
		t.Fatalf("expected deny once bucket drains")
	}

	half := now.Add(30 * time.Second)
	if !l.Allow("1.2.3.4", half) {
		t.Fatalf("expected allow after half-window refill")
	}
	if l.Allow("1.2.3.4", half) {
		t.Fatalf("expected deny after the half-window allow consumed the refill")
	}
}

func TestLimiter_IsolatedPerKey(t *testing.T) {
	l := New(2, time.Minute)
	now := time.Unix(0, 0)
	for range 2 {
		_ = l.Allow("attacker", now)
	}
	if l.Allow("attacker", now) {
		t.Fatalf("attacker bucket should be drained")
	}
	if !l.Allow("victim", now) {
		t.Fatalf("victim should be unaffected by attacker's drain")
	}
}

func TestLimiter_PeekDoesNotConsume(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Unix(0, 0)
	for range 3 {
		if !l.Peek("alice", now) {
			t.Fatalf("peek should not consume the only token")
		}
	}
	if l.Len() != 0 {
		t.Fatalf("peek created %d buckets, want 0", l.Len())
	}
	if !l.Allow("alice", now) {
		t.Fatalf("token should still be available after peeks")
	}
}

func TestLimiter_PenalizeChargesOnlyFailures(t *testing.T) {
	l := New(2, time.Minute)
	now := time.Unix(0, 0)
	l.Penalize("alice", now)
	if !l.Peek("alice", now) {
		t.Fatalf("one penalty should leave a token")
	}
	l.Penalize("alice", now)
	if l.Peek("alice", now) {
		t.Fatalf("two penalties should drain the bucket")
	}
	l.Penalize("alice", now)
	if !l.Peek("alice", now.Add(time.Minute)) {
		t.Fatalf("penalties should not accumulate past empty")
	}
}

func TestLimiter_KeySpaceIsBounded(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Unix(0, 0)
	for i := range MaxKeys {
		l.Allow(fmt.Sprintf("client-%d", i), now)
	}
	if l.Allow("one-too-many", now) {
		t.Fatalf("expected deny once the key space is exhausted")
	}
	if l.Len() > MaxKeys {
		t.Fatalf("tracked %d keys, want at most %d", l.Len(), MaxKeys)
	}
	if !l.Allow("after-gc", now.Add(10*time.Minute)) {
		t.Fatalf("idle buckets should be reclaimed for new keys")
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
			if got := ClientIP(req, tc.trusted); got != tc.want {
				t.Fatalf("ClientIP=%q want %q", got, tc.want)
			}
		})
	}
}

func TestParseTrustedProxyCIDRs(t *testing.T) {
	prefixes, err := ParseTrustedProxyCIDRs(" 10.0.0.5/8, 192.168.0.0/16, 2001:db8:1::1/48, ::ffff:10.0.0.5/104 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"10.0.0.0/8", "192.168.0.0/16", "2001:db8:1::/48", "10.0.0.0/8"}
	if len(prefixes) != len(want) {
		t.Fatalf("got %d prefixes, want %d", len(prefixes), len(want))
	}
	for i, w := range want {
		if got := prefixes[i].String(); got != w {
			t.Fatalf("prefix %d = %q, want %q", i, got, w)
		}
	}
	if empty, err := ParseTrustedProxyCIDRs(""); err != nil || empty != nil {
		t.Fatalf("empty input: %v %v", empty, err)
	}
	for _, raw := range []string{"10.0.0.1", "not-a-cidr", "::ffff:10.0.0.0/64"} {
		if _, err := ParseTrustedProxyCIDRs(raw); err == nil {
			t.Fatalf("ParseTrustedProxyCIDRs(%q) succeeded", raw)
		}
	}
}
