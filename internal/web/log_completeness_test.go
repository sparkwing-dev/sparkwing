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
	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeLogCompleteness_ReportsTheVerdictTheDashboardDraws(t *testing.T) {
	ctx := context.Background()
	logSrv, err := logs.New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(logSrv.Handler())
	defer hs.Close()
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
	if _, err := st.DB().ExecContext(ctx, `UPDATE nodes SET finished_at = ?`, time.Now().Add(-2*logs.SealGrace).UnixNano()); err != nil {
		t.Fatal(err)
	}
	client := logs.NewClient(hs.URL, nil)
	for _, id := range []string{"sealed", "silent"} {
		if err := client.Append(logs.WithAppendSequence(ctx, "s1", 1), "run-1", id, []byte("hello\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Seal(ctx, "run-1", "sealed", logs.Seal{Stream: "s1", FinalSeq: 1, Lines: 1}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		nodeLogCompletenessHandler(backend.NewStoreBackend(st, paths.Paths{Root: dir}, sparkwinglogs.New(hs.URL, nil, ""))))
	get := func(node string, want int) logs.Completeness {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/"+node+"/completeness", nil))
		if rec.Code != want {
			t.Fatalf("%s: status = %d, want %d; %s", node, rec.Code, want, rec.Body.String())
		}
		var c logs.Completeness
		_ = json.Unmarshal(rec.Body.Bytes(), &c)
		return c
	}
	if c := get("sealed", http.StatusOK); c.State != logs.StateComplete || c.Message != "" {
		t.Fatalf("sealed node = %+v", c)
	}
	if c := get("silent", http.StatusOK); c.State != logs.StateCutOff || c.Message == "" {
		t.Fatalf("unsealed node = %+v", c)
	}
	get("nope", http.StatusNotFound)

	// Negative control: a local store that keeps no seals says so.
	mux = http.NewServeMux()
	mux.HandleFunc("GET /api/v1/runs/{id}/logs/{node}/completeness",
		nodeLogCompletenessHandler(backend.NewStoreBackend(st, paths.Paths{Root: dir}, nil)))
	if c := get("silent", http.StatusOK); c.State != logs.StateUnknown {
		t.Fatalf("store without seals = %+v", c)
	}
}
