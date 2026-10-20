package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRunTrendsUseTheStoreDialect(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	now := time.Now()
	for _, run := range []struct {
		id, pipeline string
		outcomes     []string
	}{
		{id: "cached-run", pipeline: "alpha", outcomes: []string{"memoized", "memoized"}},
		{id: "mixed-run", pipeline: "alpha", outcomes: []string{"memoized", "success"}},
		{id: "other-run", pipeline: "beta", outcomes: []string{"memoized"}},
	} {
		if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{ID: run.id, Pipeline: run.pipeline, Status: "running", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		for i, outcome := range run.outcomes {
			nodeID := fmt.Sprintf("work-%d", i)
			if err := teststore.SeedNode(st, ctx, store.Node{RunID: run.id, NodeID: nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			if err := teststore.SeedNodeFinish(st, ctx, store.DefaultTeam, controllerwire.NodeRef{RunID: run.id, NodeID: nodeID}, store.NodeResult{Outcome: store.NodeState(outcome), MaskedError: ""}); err != nil {
				t.Fatal(err)
			}
		}
		if err := teststore.SeedRunFinish(st, ctx, store.DefaultTeam, run.id, "success", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{ID: "empty-run", Pipeline: "alpha", Status: "success", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	tenant, err := st.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	trends, err := tenant.ListRunTrends(ctx, now.Add(-time.Hour), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(trends) != 3 {
		t.Fatalf("trends = %+v, want three alpha runs", trends)
	}
	byID := map[string]store.RunTrend{}
	for _, trend := range trends {
		byID[trend.RunID] = trend
	}
	if !byID["cached-run"].Cached || byID["mixed-run"].Cached || byID["empty-run"].Cached {
		t.Fatalf("cached classification = %+v", byID)
	}
}
