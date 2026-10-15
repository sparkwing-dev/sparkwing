package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type revocationFixture struct {
	base  string
	store *store.Store
	auth  *Authenticator
	admin string
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	admin, _, err := st.CreateToken("root", store.TokenKindUser, []string{ScopeAdmin}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken admin: %v", err)
	}
	auth := NewAuthenticator(st, time.Minute)
	srv := httptest.NewServer(New(st, nil).WithAuthenticator(auth).Handler())
	t.Cleanup(srv.Close)
	return &revocationFixture{base: srv.URL, store: st, auth: auth, admin: admin}
}

func (f *revocationFixture) do(t *testing.T, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestRotateToken_GraceCapRejected(t *testing.T) {
	f := newRevocationFixture(t)
	_, tok, err := f.store.CreateToken("pool", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	overCap := int64(maxRotateGrace.Seconds()) + 1
	body, err := json.Marshal(map[string]any{"grace_secs": overCap})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := f.do(t, http.MethodPost, "/api/v1/tokens/"+tok.Prefix+"/rotate", f.admin, string(body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rotate over the cap = %d, want 400", resp.StatusCode)
	}

	got, err := f.store.LookupTokenByPrefix(tok.Prefix)
	if err != nil {
		t.Fatalf("LookupTokenByPrefix: %v", err)
	}
	if got.RevokedAt != nil {
		t.Fatalf("rejected rotation still stamped revoked_at = %v", got.RevokedAt)
	}
	tokens, err := f.store.ListTokens(store.TokenKindRunner, true)
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("rejected rotation minted %d runner tokens, want 1", len(tokens))
	}
}

func TestRotateToken_GraceAtCapAccepted(t *testing.T) {
	f := newRevocationFixture(t)
	_, tok, err := f.store.CreateToken("pool", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	body, err := json.Marshal(map[string]any{"grace_secs": int64(maxRotateGrace.Seconds())})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := f.do(t, http.MethodPost, "/api/v1/tokens/"+tok.Prefix+"/rotate", f.admin, string(body))
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate at the cap = %d, want 200", resp.StatusCode)
	}
}

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
