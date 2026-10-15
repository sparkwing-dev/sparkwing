package controller

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestAuthenticate_CachedEntryRechecksExpiry(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	start := time.Now().UTC()
	raw, _, err := st.CreateToken("alice", store.TokenKindUser, []string{ScopeRunsRead}, time.Minute, start)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	a := NewAuthenticator(st, time.Hour)
	clock := start
	a.now = func() time.Time { return clock }

	if _, err := a.Authenticate(raw); err != nil {
		t.Fatalf("Authenticate before expiry: %v", err)
	}
	clock = start.Add(2 * time.Minute)
	if _, err := a.Authenticate(raw); err == nil {
		t.Fatalf("cached entry authenticated an expired token")
	}
}

func TestInvalidate_LeavesOtherPrefixesCached(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	rawA, tokA, err := st.CreateToken("a", store.TokenKindUser, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken a: %v", err)
	}
	rawB, tokB, err := st.CreateToken("b", store.TokenKindUser, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken b: %v", err)
	}
	a := NewAuthenticator(st, time.Hour)
	for _, raw := range []string{rawA, rawB} {
		if _, err := a.Authenticate(raw); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	}

	a.Invalidate(tokA.Prefix)
	if _, ok := a.cache.Load(authCacheKey(rawA)); ok {
		t.Fatalf("Invalidate left the revoked prefix cached")
	}
	if _, ok := a.cache.Load(authCacheKey(rawB)); !ok {
		t.Fatalf("Invalidate dropped an unrelated prefix %q", tokB.Prefix)
	}
	a.Invalidate("")
	if _, ok := a.cache.Load(authCacheKey(rawB)); !ok {
		t.Fatalf("Invalidate(\"\") cleared the cache")
	}
}

func TestAuthenticate_InvalidateDuringLookupIsNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	raw, tok, err := st.CreateToken("victim", store.TokenKindUser, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	a := NewAuthenticator(st, time.Hour)
	var once sync.Once
	a.afterLookup = func() {
		once.Do(func() {
			if err := st.RevokeToken(tok.Prefix, time.Now().UTC()); err != nil {
				t.Errorf("RevokeToken: %v", err)
			}
			a.Invalidate(tok.Prefix)
		})
	}

	if _, err := a.Authenticate(raw); err != nil {
		t.Fatalf("in-flight Authenticate: %v", err)
	}
	if _, err := a.Authenticate(raw); err == nil {
		t.Fatalf("a token revoked mid-lookup still authenticates from the cache")
	}
}
