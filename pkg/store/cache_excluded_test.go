package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestCacheExcludedCounts_CountsCacheDominantRunsPerPipeline(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	run := func(pipeline, runID string, outcomes ...string) {
		t.Helper()
		if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: pipeline, Status: "done", StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		for i, oc := range outcomes {
			nodeID := fmt.Sprintf("n%d", i)
			if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if oc != "" {
				if err := st.FinishNode(ctx, runID, nodeID, oc, "", nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	run("alpha", "a1", "cached", "cached")
	run("alpha", "a2", "cached", "success")
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
