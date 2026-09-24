package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPostgresConcurrentExecutionStartsAndHeartbeats(t *testing.T) {
	s := openPGTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const runID = "parallel-start"
	const count = 6
	claimant := meteredClaimant(t, s, "runner:parallel")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCent, "parallel-start-credit", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRun(ctx, store.Run{ID: runID, Pipeline: "parallel", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		id := fmt.Sprintf("node-%d", i)
		if err := s.CreateNode(ctx, store.Node{RunID: runID, NodeID: id, Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkNodeReady(ctx, runID, id); err != nil {
			t.Fatal(err)
		}
	}
	claimed := make([]*store.Node, count)
	for i := range count {
		n, err := s.ClaimNextReadyNode(ctx, claimant, fmt.Sprintf("pod-%d", i), time.Minute, nil)
		if err != nil {
			t.Fatal(err)
		}
		claimed[i] = n
	}
	start := make(chan struct{})
	errs := make(chan error, count*2)
	var wg sync.WaitGroup
	for _, n := range claimed {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs <- s.AcknowledgeNodeExecutionStart(ctx, runID, n.NodeID, claimant, store.ExecutionStart{
				HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
				ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
				AttemptOrdinal: 1,
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- s.TouchNodeHeartbeat(ctx, runID, n.NodeID)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent execution start or heartbeat: %v", err)
		}
	}
}
