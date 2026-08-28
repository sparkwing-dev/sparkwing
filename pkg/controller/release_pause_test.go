package controller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func TestReleaseDebugPause_AuthDisabledFallback(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := New(st, nil)
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
