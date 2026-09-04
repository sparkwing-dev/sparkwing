package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func seedPivotRuns(t *testing.T, st *store.Store, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	for i := range n {
		id := "run-pivot-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		run := store.Run{ID: id, Pipeline: "demo", Status: "success", StartedAt: base.Add(time.Duration(i) * time.Second)}
		if err := st.CreateRun(ctx, run); err != nil {
			t.Fatalf("seed CreateRun %s: %v", id, err)
		}
	}
}

func pivotRowTotal(t *testing.T, out string) int {
	t.Helper()
	line := strings.TrimSpace(out)
	var row PipelinePivotRow
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatalf("decode pivot row %q: %v", line, err)
	}
	return row.Total
}

func TestListJobsByPipeline_HonoursLimitWithClientFilter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pivot-state.db")

	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seedPivotRuns(t, seed, 40)
	_ = seed.Close()

	p := &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}}
	total := func(filter CompiledFilter) int {
		t.Helper()
		var buf bytes.Buffer
		opts := ListOpts{
			Profile:    p,
			Limit:      5,
			JSON:       true,
			ByPipeline: true,
			Filter:     filter,
			Pivot:      PivotOpts{SparklineLen: 30, Style: SparkASCII},
		}
		if err := ListJobs(ctx, Paths{Root: t.TempDir()}, opts, &buf); err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		return pivotRowTotal(t, buf.String())
	}

	if got := total(CompiledFilter{}); got != 5 {
		t.Fatalf("unfiltered --limit 5 rolled up %d runs, want 5", got)
	}
	if got := total(CompiledFilter{StartedAfter: time.Unix(0, 0)}); got != 5 {
		t.Fatalf("--limit 5 with a client-side filter rolled up %d runs, want 5", got)
	}
}

func TestListJobsRemoteByPipeline_HonoursLimitWithClientFilter(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller-state.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	seedPivotRuns(t, st, 40)
	srv := NewControllerServer(t, st, slog.New(slog.NewTextHandler(io.Discard, nil)))

	var buf bytes.Buffer
	opts := ListOpts{
		Limit:      5,
		JSON:       true,
		ByPipeline: true,
		Filter:     CompiledFilter{StartedAfter: time.Unix(0, 0)},
		Pivot:      PivotOpts{SparklineLen: 30, Style: SparkASCII},
	}
	if err := ListJobsRemote(ctx, srv.URL, "", opts, &buf); err != nil {
		t.Fatalf("ListJobsRemote: %v", err)
	}
	if got := pivotRowTotal(t, buf.String()); got != 5 {
		t.Fatalf("remote --limit 5 with a client-side filter rolled up %d runs, want 5", got)
	}
}
