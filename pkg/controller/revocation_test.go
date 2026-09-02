package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	st, err := store.Open(filepath.Join(dir, "state.db"))
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

func (f *revocationFixture) whoamiStatus(t *testing.T, token string) int {
	t.Helper()
	resp := f.do(t, http.MethodGet, "/api/v1/auth/whoami", token, "")
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestRevokeToken_TakesEffectOnTheSameReplica(t *testing.T) {
	f := newRevocationFixture(t)
	raw, tok, err := f.store.CreateToken("victim", store.TokenKindUser, []string{ScopeRunsRead}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusOK {
		t.Fatalf("whoami before revoke = %d, want 200", got)
	}

	resp := f.do(t, http.MethodDelete, "/api/v1/tokens/"+tok.Prefix, f.admin, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", resp.StatusCode)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusUnauthorized {
		t.Fatalf("whoami after revoke = %d, want 401", got)
	}
}

func TestRotateThenRevoke_CutsTheGraceWindowShort(t *testing.T) {
	f := newRevocationFixture(t)
	raw, tok, err := f.store.CreateToken("pool", store.TokenKindRunner, []string{ScopeNodesClaim}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusOK {
		t.Fatalf("whoami before rotate = %d, want 200", got)
	}

	resp := f.do(t, http.MethodPost, "/api/v1/tokens/"+tok.Prefix+"/rotate", f.admin, `{"grace_secs":86400}`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate = %d, want 200", resp.StatusCode)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusOK {
		t.Fatalf("whoami during grace = %d, want 200", got)
	}

	resp = f.do(t, http.MethodDelete, "/api/v1/tokens/"+tok.Prefix, f.admin, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke during grace = %d, want 204", resp.StatusCode)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusUnauthorized {
		t.Fatalf("whoami after early revoke = %d, want 401", got)
	}
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

func TestDeleteUser_InvalidatesSessionAndTokens(t *testing.T) {
	f := newRevocationFixture(t)
	now := time.Now().UTC()
	if _, err := f.store.CreateUser("mallory", "correct-horse", []string{ScopeAdmin}, now); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	rawSession, _, _, err := f.store.CreateSession("mallory", []string{ScopeAdmin}, time.Hour, now)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	raw, _, err := f.store.CreateToken("mallory", store.TokenKindUser, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusOK {
		t.Fatalf("whoami before delete = %d, want 200", got)
	}

	resp := f.do(t, http.MethodDelete, "/api/v1/users/mallory", f.admin, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete user = %d, want 204", resp.StatusCode)
	}
	if got := f.whoamiStatus(t, raw); got != http.StatusUnauthorized {
		t.Fatalf("whoami after delete = %d, want 401", got)
	}
	if _, err := f.store.LookupSession(rawSession, now.Add(time.Second)); err == nil {
		t.Fatalf("session of a deleted user still resolves")
	}
}

func TestAuthenticate_CachedEntryRechecksExpiry(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
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
	st, err := store.Open(filepath.Join(dir, "state.db"))
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
	if _, ok := a.cache.Load(rawA); ok {
		t.Fatalf("Invalidate left the revoked prefix cached")
	}
	if _, ok := a.cache.Load(rawB); !ok {
		t.Fatalf("Invalidate dropped an unrelated prefix %q", tokB.Prefix)
	}
	a.Invalidate("")
	if _, ok := a.cache.Load(rawB); !ok {
		t.Fatalf("Invalidate(\"\") cleared the cache")
	}
}
