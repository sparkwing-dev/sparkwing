package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if !l.Allow("one-too-many", now) {
		t.Fatalf("a full key space must evict, not deny an unseen client")
	}
	if l.Len() > MaxKeys {
		t.Fatalf("tracked %d keys, want at most %d", l.Len(), MaxKeys)
	}
	if !l.Allow("after-gc", now.Add(10*time.Minute)) {
		t.Fatalf("idle buckets should be reclaimed for new keys")
	}
}

func TestLimiter_EvictionKeepsServingNewClients(t *testing.T) {
	l := New(1, time.Minute)
	now := time.Unix(0, 0)
	for i := range MaxKeys {
		l.Allow(fmt.Sprintf("client-%d", i), now.Add(time.Duration(i)*time.Microsecond))
	}
	warm := fmt.Sprintf("client-%d", MaxKeys-1)
	warmAt := now.Add(time.Duration(MaxKeys-1) * time.Microsecond)
	if l.Peek(warm, warmAt) {
		t.Fatalf("the warm bucket should already be drained")
	}

	flood := now.Add(time.Second)
	for i := range 100 {
		fresh := fmt.Sprintf("fresh-%d", i)
		if !l.Allow(fresh, flood) {
			t.Fatalf("%s denied at a full key space", fresh)
		}
		if l.Len() > MaxKeys {
			t.Fatalf("tracked %d keys after %s, want at most %d", l.Len(), fresh, MaxKeys)
		}
	}
	// safety: the coldest buckets go first, so a key flood cannot forget the client that just spent its budget.
	if l.Peek(warm, flood) {
		t.Fatalf("a recently used drained bucket was evicted, handing the flood a fresh budget")
	}
}

func TestClientIP_HonorsForwardedAddressOnlyOnTheTrustedListener(t *testing.T) {
	cases := []struct {
		name       string
		trusted    bool
		realIPs    []string
		remoteAddr string
		want       string
	}{
		{name: "trusted listener uses forwarded address", trusted: true, realIPs: []string{"198.51.100.7"}, remoteAddr: "10.0.0.1:5000", want: "198.51.100.7"},
		{name: "IPv6 forwarded address", trusted: true, realIPs: []string{"2001:db8::7"}, remoteAddr: "10.0.0.1:5000", want: "2001:db8::7"},
		{name: "plain listener ignores spoofed address", realIPs: []string{"198.51.100.7"}, remoteAddr: "203.0.113.9:5000", want: "203.0.113.9"},
		{name: "second address copy is refused", trusted: true, realIPs: []string{"198.51.100.7", "198.51.100.8"}, remoteAddr: "10.0.0.1:5000", want: "10.0.0.1"},
		{name: "malformed forwarded address uses peer", trusted: true, realIPs: []string{"unknown"}, remoteAddr: "10.0.0.1:5000", want: "10.0.0.1"},
		{name: "trusted listener without the header uses peer", trusted: true, remoteAddr: "10.0.0.1:5000", want: "10.0.0.1"},
		{name: "peer without port", remoteAddr: "127.0.0.1", want: "127.0.0.1"},
		{name: "malformed peer stays opaque", trusted: true, remoteAddr: "local-peer", want: "local-peer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", "192.0.2.66")
			for _, v := range tc.realIPs {
				req.Header.Add("X-Real-IP", v)
			}
			var got string
			var h http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ClientIP(r) })
			if tc.trusted {
				h = TrustedListener(h)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("ClientIP=%q want %q", got, tc.want)
			}
		})
	}
}

func TestAllowWithRetry_NamesTheRealRefillAndLengthensUnderPressure(t *testing.T) {
	l := New(2, time.Minute)
	start := time.Now()

	for i := range 2 {
		ok, wait := l.AllowWithRetry("runner", start)
		if !ok || wait != 0 {
			t.Fatalf("attempt %d: allowed=%v wait=%s, want allowed with no wait", i, ok, wait)
		}
	}

	ok, first := l.AllowWithRetry("runner", start)
	if ok {
		t.Fatal("a third attempt inside the window was allowed")
	}
	if first < 30*time.Second {
		t.Errorf("wait=%s; a bucket of 2 a minute refills one token in 30s", first)
	}

	_, second := l.AllowWithRetry("runner", start)
	if second <= first {
		t.Errorf("wait did not lengthen under pressure: %s then %s", first, second)
	}
}

func TestAllowWithRetry_BoundsTheDebtOneKeyCanBuild(t *testing.T) {
	l := New(4, time.Minute)
	start := time.Now()
	for range 4 {
		l.AllowWithRetry("runner", start)
	}
	var longest time.Duration
	for range 200 {
		_, wait := l.AllowWithRetry("runner", start)
		longest = max(longest, wait)
	}
	if longest > 2*time.Minute {
		t.Errorf("longest wait=%s; a capped debt cannot exceed a window plus its own refill", longest)
	}
}
