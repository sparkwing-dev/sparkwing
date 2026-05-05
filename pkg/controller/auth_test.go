package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

func teapotHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
}

func newStoreForAuth(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAuthenticator_Disabled exercises the "auth off" path: nil store
// means every request is let through. Preserves the laptop-local dev
// invariant (empty tokens table = pass-through).
func TestAuthenticator_Disabled(t *testing.T) {
	a := NewAuthenticator(nil, 0)
	if !a.AuthDisabled() {
		t.Fatalf("expected AuthDisabled=true with nil store")
	}
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected pass-through 418, got %d", rec.Code)
	}
}

// TestAuthenticator_MissingHeader returns 401 when auth is enabled
// and no Authorization header is present.
func TestAuthenticator_MissingHeader(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), 0)
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// TestAuthenticator_WrongScheme rejects non-Bearer schemes. Basic
// auth shouldn't accidentally unlock the controller.
func TestAuthenticator_WrongScheme(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), 0)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Basic dG9rMQ==")
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

// TestAuthenticator_NonSwPrefixRejected ensures tokens without a
// known sw*_ prefix fail authentication. Closes the v0 fallback
// path that phase 4 of FOLLOWUPS #2 removed.
func TestAuthenticator_NonSwPrefixRejected(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), 0)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer legacy-shared-secret-123")
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for non-sw prefix, got %d", rec.Code)
	}
}

// TestAuthenticator_StoreToken exercises the sw*_ prefix path: a real
// token row authenticates; an unknown sw*_ token 401s.
func TestAuthenticator_StoreToken(t *testing.T) {
	st := newStoreForAuth(t)
	now := time.Now().UTC()
	raw, _, err := st.CreateToken("alice", store.TokenKindUser, []string{ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	a := NewAuthenticator(st, 0)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("authed: expected 418, got %d", rec.Code)
	}

	// A well-formed but unknown token (correct prefix, random tail) 401s.
	req = httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer swu_unknownXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
	rec = httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown store token: expected 401, got %d", rec.Code)
	}
}

// TestRequireScope_Allowed: principal has scope -> handler runs.
func TestRequireScope_Allowed(t *testing.T) {
	inner := teapotHandler()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	p := &Principal{Name: "alice", Kind: "user", Scopes: []string{ScopeRunsRead}}
	req = req.WithContext(contextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	requireScope(ScopeRunsRead, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected 418, got %d", rec.Code)
	}
}

// TestRequireScope_Forbidden: principal without scope -> 403.
func TestRequireScope_Forbidden(t *testing.T) {
	inner := teapotHandler()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	p := &Principal{Name: "runner", Kind: "runner", Scopes: []string{ScopeNodesClaim}}
	req = req.WithContext(contextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	requireScope(ScopeRunsRead, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

// TestRequireScope_AdminIsSuperset: admin scope unlocks anything.
func TestRequireScope_AdminIsSuperset(t *testing.T) {
	inner := teapotHandler()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	p := &Principal{Name: "alice", Kind: "user", Scopes: []string{ScopeAdmin}}
	req = req.WithContext(contextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	requireScope(ScopeRunsRead, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("admin should be superset: got %d", rec.Code)
	}
}

// TestRequireScope_NoPrincipalPassesThrough: when auth is disabled
// the context has no principal; requireScope must not 401.
func TestRequireScope_NoPrincipalPassesThrough(t *testing.T) {
	inner := teapotHandler()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	requireScope(ScopeAdmin, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("no-principal should pass-through, got %d", rec.Code)
	}
}
