package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

// A fanned-out run closes several claim rounds at once while a credit-refused
// claim records its refusal on the same run; neither may abort the other.
func TestFinalizeClaimRoundSurvivesConcurrentRunEvents(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	const runID, nodes = "fanout", 7
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "example", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := range nodes {
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: fmt.Sprintf("n%d", i), Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	for round := range 15 {
		closedWindow := time.Now().Add(-time.Hour).UnixNano()
		if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE nodes SET ready_at = ?, offer_started_at = ?
 WHERE run_id = ?`), closedWindow, closedWindow, runID); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2*nodes)
		for i := range nodes {
			nodeID := fmt.Sprintf("n%d", i)
			wg.Go(func() {
				if _, err := st.FinalizeExecutorClaimRound(ctx, runID, nodeID); err != nil {
					errs <- fmt.Errorf("round %d finalize %s: %w", round, nodeID, err)
				}
			})
			wg.Go(func() {
				kind := fmt.Sprintf("credits_blocked_%d", round)
				if _, err := st.AppendEventOnce(ctx, runID, nodeID, kind, []byte(`{}`)); err != nil {
					errs <- fmt.Errorf("round %d append event %s: %w", round, nodeID, err)
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if t.Failed() {
			return
		}
	}
}
