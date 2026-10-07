package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	var row PipelinePivotRow
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatalf("decode pivot row %q: %v", line, err)
	}
	return row.Total
}

func TestListJobsByPipeline_CountsASubsetAcrossPages(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subset-state.db")

	seed, err := teststore.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	const successes, failures = store.MaxRunListLimit + 25, 15
	for i := range successes + failures {
		status := "success"
		if i >= successes {
			status = "failed"
		}
		run := store.Run{
			ID:        fmt.Sprintf("run-subset-%04d", i),
			Pipeline:  "demo",
			Status:    status,
			StartedAt: base.Add(time.Duration(i) * time.Second),
		}
		if err := seed.CreateRun(ctx, run); err != nil {
			t.Fatalf("seed CreateRun: %v", err)
		}
	}
	_ = seed.Close()

	var buf bytes.Buffer
	opts := ListOpts{
		Profile:    &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}},
		Limit:      5,
		Statuses:   []string{"success"},
		JSON:       true,
		ByPipeline: true,
		Pivot:      PivotOpts{SparklineLen: 30, Style: SparkASCII},
	}
	if err := ListJobs(ctx, Paths{Root: t.TempDir()}, opts, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if got := pivotRowTotal(t, buf.String()); got != successes {
		t.Fatalf("rollup counted %d successful runs, want %d", got, successes)
	}
}

func TestListJobsByPipeline_SummarySaysWhenTotalsStopShort(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "summary-state.db")
	seed, err := teststore.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	seedPivotRuns(t, seed, 40)
	_ = seed.Close()

	var buf bytes.Buffer
	opts := ListOpts{
		Profile:    &profile.Profile{Name: "local", State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath}},
		Limit:      5,
		JSON:       true,
		ByPipeline: true,
		Pivot:      PivotOpts{SparklineLen: 30, Style: SparkASCII},
	}
	if err := ListJobs(ctx, Paths{Root: t.TempDir()}, opts, &buf); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	var summary PipelinePivotSummary
	found := false
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.Contains(line, `"kind":"summary"`) {
			continue
		}
		if err := json.Unmarshal([]byte(line), &summary); err != nil {
			t.Fatalf("decode summary %q: %v", line, err)
		}
		found = true
	}
	if !found {
		t.Fatal("rollup carried no kind:summary record, so a program cannot tell complete totals from capped ones")
	}
	if summary.Truncated {
		t.Errorf("a rollup that read every matching run reported truncated=true (%s)", summary.Reason)
	}
}
