package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestReceiptEndpoint_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	start := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-recpt-1", Pipeline: "deploy", Status: "running",
		StartedAt:    start,
		Args:         map[string]string{"env": "prod"},
		PlanSnapshot: []byte(`{"nodes":["build","deploy"]}`),
		GitSHA:       "abc1234",
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = st.DB().ExecContext(ctx,
		`UPDATE runs SET status = 'success', finished_at = ? WHERE id = ?`,
		end.UnixNano(), "run-recpt-1")

	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-recpt-1", NodeID: "build", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{
		RunID: "run-recpt-1", NodeID: "deploy", Status: "pending", Deps: []string{"build"},
	}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, st, "UPDATE nodes SET status='done', outcome='success', started_at=?, finished_at=?, output_json=? WHERE node_id=?",
		start.UnixNano(), start.Add(1*time.Hour).UnixNano(), []byte(`{"img":"x"}`), "build")
	mustExec(t, st, "UPDATE nodes SET status='done', outcome='success', started_at=?, finished_at=?, output_json=? WHERE node_id=?",
		start.Add(1*time.Hour).UnixNano(), end.UnixNano(), []byte(`{"ok":true}`), "deploy")

	handler := controller.New(st, nil).
		WithCostRate(0.05, "controller config (cost_per_runner_hour=$0.05)").
		Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-recpt-1/receipt", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("receipt: %d %s", response.Code, response.Body)
	}
	body := response.Body.Bytes()
	var rec map[string]any
	if err := json.Unmarshal(body, &rec); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, body)
	}
	for _, key := range []string{"run_id", "pipeline", "git_sha", "status", "started_at", "finished_at", "duration_ms", "identity", "steps", "cost", "receipt_sha"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("missing top-level field %q", key)
		}
	}
	id, _ := rec["identity"].(map[string]any)
	for _, key := range []string{"pipeline_version_hash", "inputs_hash", "plan_hash", "outputs_hash"} {
		v, ok := id[key]
		if !ok || v == nil {
			t.Errorf("identity.%s missing", key)
		}
	}
	cost, _ := rec["cost"].(map[string]any)
	if cost["currency"] != "USD" {
		t.Errorf("cost.currency = %v, want USD", cost["currency"])
	}
	if cents, _ := cost["compute_cents"].(float64); cents != 10 {
		t.Errorf("cost.compute_cents = %v, want 10", cost["compute_cents"])
	}
	if got, _ := cost["rate_source"].(string); !strings.Contains(got, "cost_per_runner_hour") {
		t.Errorf("cost.rate_source = %q, missing rate provenance", got)
	}
	if settled, _ := cost["settled"].(bool); settled {
		t.Error("cost.settled should default false")
	}
	if sha, _ := rec["receipt_sha"].(string); !strings.HasPrefix(sha, "sha256:") {
		t.Errorf("receipt_sha = %q, want sha256: prefix", sha)
	}
}

func TestReceiptEndpoint_NotFound(t *testing.T) {
	dir := t.TempDir()
	st, err := teststore.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	response := httptest.NewRecorder()
	controller.New(st, nil).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/missing/receipt", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", response.Code, response.Body)
	}
}

func mustExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
