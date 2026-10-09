package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type manifestVerdicts struct {
	storage.LogStore
	verdicts map[string]logseg.Completeness
}

func (m manifestVerdicts) NodeLogCompleteness(_ context.Context, _, nodeID string, _ logseg.NodeProgress, _ time.Time) (logseg.Completeness, bool, error) {
	c, ok := m.verdicts[nodeID]
	return c, ok, nil
}

func TestNodeLogCompleteness_ReportsTheVerdictTheDashboardDraws(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sealed", "silent"} {
		if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.StartNode(ctx, "run-1", id); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishNode(ctx, "run-1", id, "success", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	logStore := manifestVerdicts{verdicts: map[string]logseg.Completeness{
		"sealed": {State: logseg.StateComplete, Lines: 1},
		"silent": {State: logseg.StateCutOff, Lines: 1, Message: "the log stopped before its writer finished"},
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		NodeLogCompletenessHandler(backend.NewStoreBackend(st, paths.Paths{Root: dir}, logStore)))
	get := func(node string, want int) logseg.Completeness {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/"+node+"/completeness", nil))
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d; %s", node, rec.Code, want, rec.Body.String())
		}
		var c logseg.Completeness
		_ = json.Unmarshal(rec.Body.Bytes(), &c)
		return c
	}
	if c := get("sealed", http.StatusOK); c.State != logseg.StateComplete || c.Message != "" {
		t.Fatalf("sealed node = %+v", c)
	}
	if c := get("silent", http.StatusOK); c.State != logseg.StateCutOff || c.Message == "" {
		t.Fatalf("unsealed node = %+v", c)
	}
	get("nope", http.StatusNotFound)

	mux = http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		NodeLogCompletenessHandler(backend.NewStoreBackend(st, paths.Paths{Root: dir}, nil)))
	if c := get("silent", http.StatusOK); c.State != logseg.StateUnknown {
		t.Fatalf("store without manifests = %+v", c)
	}
}
