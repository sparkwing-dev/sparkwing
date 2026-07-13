package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestCacheExcludedCounts_CountsFullyCachedRunsPerPipeline(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	run := func(pipeline, runID string, outcomes ...string) []string {
		t.Helper()
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: pipeline, Status: "done", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		nodeIDs := make([]string, 0, len(outcomes))
		for i, oc := range outcomes {
			nodeID := fmt.Sprintf("n%d", i)
			nodeIDs = append(nodeIDs, nodeID)
			if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if oc != "" {
				if err := st.FinishNode(ctx, runID, nodeID, oc, "", nil); err != nil {
					t.Fatal(err)
				}
			}
		}
		return nodeIDs
	}

	run("alpha", "a1", "cached", "cached")
	run("alpha", "a2", "cached", "success")
	measured := run("alpha", "a3", append([]string{"success"}, repeat("cached", 19)...)...)
	if err := st.AddNodeMetricSample(ctx, "a3", measured[0], store.MetricSample{
		TS: time.Now(), CPUMillicores: 1200, MemoryBytes: 128 << 20,
	}); err != nil {
		t.Fatal(err)
	}
	run("beta", "b1", "cached")
	run("beta", "b2", "success", "success")

	counts, err := st.CacheExcludedCounts(ctx, "", "cached", 0.9)
	if err != nil {
		t.Fatalf("CacheExcludedCounts: %v", err)
	}
	if counts["alpha"] != 1 {
		t.Errorf("alpha count = %d, want 1 (only the fully-cached run)", counts["alpha"])
	}
	if counts["beta"] != 1 {
		t.Errorf("beta count = %d, want 1", counts["beta"])
	}

	scoped, err := st.CacheExcludedCounts(ctx, "alpha", "cached", 0.9)
	if err != nil {
		t.Fatalf("scoped CacheExcludedCounts: %v", err)
	}
	if scoped["alpha"] != 1 {
		t.Errorf("scoped alpha count = %d, want 1", scoped["alpha"])
	}
	if _, ok := scoped["beta"]; ok {
		t.Errorf("scoped query leaked beta: %v", scoped)
	}
}

func repeat(v string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = v
	}
	return out
}
