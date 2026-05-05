package controller

// handleReleaseDebugPause must derive released_by from the
// authenticated principal, not the request body, so the audit row
// tracks who actually performed the release.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// newPauseTestServer wires up a Server backed by a fresh SQLite store
// with auth enabled (one user-scoped token). Returns the Server, the
// raw bearer token, and the store handle for assertions.
func newPauseTestServer(t *testing.T) (*Server, string, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	raw, _, err := st.CreateToken("alice", store.TokenKindUser,
		[]string{ScopeRunsWrite}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	srv := New(st, nil).WithAuthenticator(NewAuthenticator(st, 0))
	return srv, raw, st
}

// seedActivePause inserts a run + node + open debug_pause row so the
// release endpoint has something to flip.
func seedActivePause(t *testing.T, st *store.Store, runID, nodeID string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: runID, Pipeline: "p", Status: "running",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: runID, NodeID: nodeID, Status: "paused",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.CreateDebugPause(ctx, store.DebugPause{
		RunID:     runID,
		NodeID:    nodeID,
		Reason:    "manual",
		PausedAt:  time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateDebugPause: %v", err)
	}
}

// assertReleasedBy reads back the pause row and checks the audit
// column matches expected.
func assertReleasedBy(t *testing.T, st *store.Store, runID, nodeID, want string) {
	t.Helper()
	rows, err := st.ListDebugPauses(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListDebugPauses: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 pause row, got %d", len(rows))
	}
	if rows[0].ReleasedAt == nil {
		t.Fatalf("expected released_at to be set")
	}
	if rows[0].ReleasedBy != want {
		t.Fatalf("released_by=%q want %q", rows[0].ReleasedBy, want)
	}
}

// TestReleaseDebugPause_RecordsAuthPrincipal: with a valid bearer
// token, the released_by column gets the principal name regardless of
// what the body says.
func TestReleaseDebugPause_RecordsAuthPrincipal(t *testing.T) {
	srv, raw, st := newPauseTestServer(t)
	seedActivePause(t, st, "run-1", "node-a")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-1/nodes/node-a/release", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s want 204", rec.Code, rec.Body.String())
	}
	assertReleasedBy(t, st, "run-1", "node-a", "alice")
}

// TestReleaseDebugPause_AuthOverridesBody: the auth principal wins
// even when the request body tries to spoof a different released_by.
func TestReleaseDebugPause_AuthOverridesBody(t *testing.T) {
	srv, raw, st := newPauseTestServer(t)
	seedActivePause(t, st, "run-2", "node-b")

	body := bytes.NewReader([]byte(`{"released_by":"mallory","release_kind":"manual"}`))
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-2/nodes/node-b/release", body)
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s want 204", rec.Code, rec.Body.String())
	}
	assertReleasedBy(t, st, "run-2", "node-b", "alice")
}

// TestReleaseDebugPause_Unauthenticated: with auth enabled and no
// bearer header, the request is rejected before it can touch the
// store. The pause row stays open.
func TestReleaseDebugPause_Unauthenticated(t *testing.T) {
	srv, _, st := newPauseTestServer(t)
	seedActivePause(t, st, "run-3", "node-c")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-3/nodes/node-c/release", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}

	rows, err := st.ListDebugPauses(context.Background(), "run-3")
	if err != nil {
		t.Fatalf("ListDebugPauses: %v", err)
	}
	if len(rows) != 1 || rows[0].ReleasedAt != nil {
		t.Fatalf("pause should still be open after 401")
	}
}

// TestReleaseDebugPause_AuthDisabledFallback: with auth disabled
// (laptop dev, no tokens minted), the released_by column falls back
// to "anonymous" instead of an empty string so the audit row stays
// meaningful.
func TestReleaseDebugPause_AuthDisabledFallback(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := New(st, nil) // no authenticator => disabled
	seedActivePause(t, st, "run-4", "node-d")

	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/runs/run-4/nodes/node-d/release", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s want 204", rec.Code, rec.Body.String())
	}
	assertReleasedBy(t, st, "run-4", "node-d", "anonymous")
}
