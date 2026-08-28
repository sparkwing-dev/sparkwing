package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func TestAuthenticator_MissingHeader(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), 0)
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

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

	req = httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer swu_unknownXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
	rec = httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown store token: expected 401, got %d", rec.Code)
	}
}

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

func TestRequireScope_NoPrincipalPassesThrough(t *testing.T) {
	inner := teapotHandler()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	requireScope(ScopeAdmin, inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("no-principal should pass-through, got %d", rec.Code)
	}
}

func TestRequireScope_ForbiddenBodyShape(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	p := &Principal{Name: "warm-runner-7", Kind: "runner", Scopes: []string{ScopeNodesClaim}}
	req = req.WithContext(contextWithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	requireScope(ScopeRunsRead, teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var body authErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v -- raw=%q", err, rec.Body.String())
	}
	if body.Code != "missing_scope" {
		t.Errorf("Code: got %q, want missing_scope", body.Code)
	}
	if body.MissingScope != ScopeRunsRead {
		t.Errorf("MissingScope: got %q, want %q", body.MissingScope, ScopeRunsRead)
	}
	if body.Principal != "runner:warm-runner-7" {
		t.Errorf("Principal: got %q, want runner:warm-runner-7", body.Principal)
	}
	if body.Message == "" {
		t.Errorf("Message must be non-empty for human-readable fallback")
	}
}

func TestMiddleware_UnauthenticatedBodyShape(t *testing.T) {
	a := NewAuthenticator(newStoreForAuth(t), 0)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	a.Middleware(teapotHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
	var body authErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v -- raw=%q", err, rec.Body.String())
	}
	if body.Code != "unauthenticated" {
		t.Errorf("Code: got %q, want unauthenticated", body.Code)
	}
	if body.MissingScope != "" {
		t.Errorf("MissingScope must be empty on 401, got %q", body.MissingScope)
	}
	if body.Principal != "" {
		t.Errorf("Principal must be empty on 401, got %q", body.Principal)
	}
	if body.Message == "" {
		t.Errorf("Message must be non-empty")
	}
}
