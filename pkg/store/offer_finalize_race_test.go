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

// An executor offer appends its offer_received event, which takes the run's
// event-sequence lock, and then awards under the run's row lock; a deadline
// round takes the run's row lock and then appends its events. Both run on
// one run at once whenever a fanned-out run is offered and finalized
// together, and neither may abort the other.
func TestExecutorOffersSurviveConcurrentFinalizeOnOneRun(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	const runID, width, rounds = "race", 6, 8
	if err := st.CreateRun(ctx, store.Run{ID: runID, Pipeline: "example", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	ready := func(nodeID string) {
		if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: nodeID, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkNodeReady(ctx, runID, nodeID); err != nil {
			t.Fatal(err)
		}
	}
	for round := range rounds {
		closedWindow := time.Now().Add(-time.Hour).UnixNano()
		type offerer struct {
			identity store.ClaimIdentity
			name     string
			nodeID   string
		}
		offers := make([]offerer, 0, width)
		finals := make([]string, 0, width)
		for i := range width {
			finalNode := fmt.Sprintf("f%d-%d", round, i)
			ready(finalNode)
			if _, err := st.DB().Exec(storetest.Rebind(st, `UPDATE nodes SET ready_at = ?, offer_started_at = ?
 WHERE run_id = ? AND node_id = ?`), closedWindow, closedWindow, runID, finalNode); err != nil {
				t.Fatal(err)
			}
			finals = append(finals, finalNode)
			offerNode := fmt.Sprintf("o%d-%d", round, i)
			ready(offerNode)
			name := fmt.Sprintf("exec-%d-%d", round, i)
			offers = append(offers, offerer{enrollOfferExecutor(t, st, name, 100, 100), name, offerNode})
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2*width)
		for i := range width {
			o, finalNode := offers[i], finals[i]
			wg.Go(func() {
				summary, err := st.SchedulingSummary(ctx, runID, o.nodeID)
				if err != nil {
					errs <- fmt.Errorf("round %d summary %s: %w", round, o.nodeID, err)
					return
				}
				if _, err := st.TestOnlyOfferExecutorClaim(ctx, o.identity, store.ExecutorClaimOffer{
					ExecutorName: o.name, HolderID: "holder-" + o.name, RunID: runID, NodeID: o.nodeID,
					ReservationID: "reservation-" + o.name, ResourceDigest: summary.ResourceDigest,
					Slot: 0, Lease: time.Minute,
				}); err != nil {
					errs <- fmt.Errorf("round %d offer %s: %w", round, o.nodeID, err)
				}
			})
			wg.Go(func() {
				if _, err := st.FinalizeExecutorClaimRound(ctx, runID, finalNode); err != nil {
					errs <- fmt.Errorf("round %d finalize %s: %w", round, finalNode, err)
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
