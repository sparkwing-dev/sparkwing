package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type dispatchRouteFixture struct {
	st      *store.Store
	handler http.Handler
}

const dispatchHash = `"spec_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`

func newDispatchRouteFixture(t *testing.T) dispatchRouteFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).CreateRun(ctx, store.Run{ID: "run-d", Pipeline: "demo", Status: "pending", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	return dispatchRouteFixture{st: st, handler: New(st, nil).Handler()}
}

func (f dispatchRouteFixture) claim(t *testing.T, nodeID string, kind store.ClaimTokenKind) string {
	t.Helper()
	if kind == store.ClaimTokenPlan {
		nodeID = store.PlanNodeID
	}
	return LauncherFixtureClaim(t, f.st, "run-d", nodeID)
}

func (f dispatchRouteFixture) post(t *testing.T, path, bearer, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func wantPost(t *testing.T, f dispatchRouteFixture, path, bearer, body string, want int, status string) {
	t.Helper()
	code, out := f.post(t, path, bearer, body)
	if code != want || (status != "" && out["status"] != status) {
		t.Fatalf("POST %s: %d %v, want %d %q", path, code, out, want, status)
	}
}

const (
	planPath    = "/api/v1/runs/run-d/plan"
	attemptPath = "/api/v1/runs/run-d/nodes/a/attempt"
)
