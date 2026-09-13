package controller

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAuthenticate_ConcurrentClaimsVerifyOnce(t *testing.T) {
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	raw, _, err := st.CreateToken("pool", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	a := NewAuthenticator(st, time.Minute)
	var lookups atomic.Int64
	a.afterLookup = func() { lookups.Add(1) }

	const callers = 24
	start := make(chan struct{})
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, err := a.Authenticate(raw)
			if err == nil && p.Name != "pool" {
				t.Errorf("principal = %q, want pool", p.Name)
			}
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatalf("concurrent Authenticate: %v", err)
		}
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("token lookups = %d for %d concurrent claims, want 1", got, callers)
	}
}

func TestAuthenticate_RevokedTokenStopsWithinTheCacheWindow(t *testing.T) {
	st := newStoreForAuth(t)
	start := time.Now().UTC()
	raw, tok, err := st.CreateToken("pool", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, start)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	const window = time.Minute
	a := NewAuthenticator(st, window)
	clock := start
	a.now = func() time.Time { return clock }

	if _, err := a.Authenticate(raw); err != nil {
		t.Fatalf("Authenticate before revoke: %v", err)
	}
	// safety: a revoke another replica served reaches this one only through the row, which is what bounds the window.
	if err := st.RevokeToken(tok.Prefix, start); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	clock = start.Add(window - time.Second)
	if _, err := a.Authenticate(raw); err != nil {
		t.Fatalf("Authenticate inside the cache window: %v", err)
	}
	clock = start.Add(window + time.Second)
	if _, err := a.Authenticate(raw); err == nil {
		t.Fatalf("a token revoked %s ago still authenticates", window)
	}
}

func TestAuthCacheKey_KeepsTheSecretHalfOutOfMemory(t *testing.T) {
	raw := "swr_" + strings.Repeat("s", 43)
	key := authCacheKey(raw)

	if !strings.HasPrefix(key, raw[:store.PrefixLen]+":") {
		t.Fatalf("cache key %q does not open with the public prefix", key)
	}
	if strings.Contains(key, raw[store.PrefixLen:]) {
		t.Fatalf("cache key %q carries the secret half of the token", key)
	}
	if key == authCacheKey(raw+"x") {
		t.Fatalf("two tokens share the cache key %q", key)
	}
}
