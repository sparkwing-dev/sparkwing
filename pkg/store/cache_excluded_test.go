package store_test

import (
	"context"
	"fmt"
	"testing"

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
	number := 0

	run := func(pipeline, runID string, outcomes ...string) {
		t.Helper()
		number++
		_, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO runs
 (team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days)
 VALUES (?,?,?,?,'success',?,'request',1,'gh:1','host','execute',?,'fixture',1,2,'r30',30)`), store.DefaultTeam, runID, number, pipeline, []byte(`{}`), []byte(`[]`))
		if err != nil {
			t.Fatal(err)
		}
		for i, oc := range outcomes {
			nodeID := fmt.Sprintf("n%d", i)
			status := "pending"
			var finished any
			if oc != "" {
				status, finished = "done", int64(2)
			}
			_, err := st.DB().ExecContext(ctx, storetest.Rebind(st, `INSERT INTO nodes
 (team,run_id,node_id,status,outcome,finished_at,spec_json,execution_json) VALUES (?,?,?,?,?,?,?,?)`), store.DefaultTeam, runID, nodeID, status, oc, finished, []byte(`{}`), []byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	run("alpha", "a1", "memoized", "memoized")
	run("alpha", "a2", "memoized", "success")
	run("beta", "b1", "memoized")
	run("beta", "b2", "success", "success")

	counts, err := tenantFor(t, st, store.DefaultTeam).CacheExcludedCounts(ctx, "", "memoized", 0.9)
	if err != nil {
		t.Fatalf("CacheExcludedCounts: %v", err)
	}
	if counts["alpha"] != 1 {
		t.Errorf("alpha count = %d, want 1 (only the fully-cached run)", counts["alpha"])
	}
	if counts["beta"] != 1 {
		t.Errorf("beta count = %d, want 1", counts["beta"])
	}

	scoped, err := tenantFor(t, st, store.DefaultTeam).CacheExcludedCounts(ctx, "alpha", "memoized", 0.9)
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
