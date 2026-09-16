package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPostgresCancellationKeepsReservationUntilExecutionStartIsFenced(t *testing.T) {
	s := openPGTestStore(t)
	ctx := context.Background()
	claimant := meteredClaimant(t, s, "agent:cloud")
	readyNode(t, s, "run-cancel-start", "build")
	if _, err := s.GrantCredits(ctx, store.CreditGrantPaid,
		100*store.MicroCreditsPerCredit, "pay_cancel_start", "admin"); err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNextReadyNode(ctx, claimant, "pod-1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}

	blocker, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, "sparkwing/executor-eligibility"); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
	}
	cancelled := make(chan error, 1)
	go func() {
		cancelled <- s.CancelNodeForComputeLimit(ctx, n.RunID, n.NodeID,
			claimant.TokenPrefix, "runner_limit", time.Now().Add(2*time.Second))
	}()

	waiting := false
	for range 1000 {
		var count int
		if err := s.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&count); err != nil {
			_ = blocker.Rollback()
			t.Fatal(err)
		}
		if count > 0 {
			waiting = true
			break
		}
	}
	if !waiting {
		_ = blocker.Rollback()
		t.Fatal("cancellation did not wait on executor eligibility")
	}
	if anchor := chargeWindowAnchor(t, s, n.RunID, n.NodeID); anchor == 0 {
		_ = blocker.Rollback()
		t.Fatal("cancellation refunded the reservation before fencing execution start")
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, claimant, store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		AttemptOrdinal: n.AttemptsConsumed + 1,
	}); err != nil {
		_ = blocker.Rollback()
		t.Fatalf("execution start while cancellation waited: %v", err)
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}

	charges, err := s.ListCreditCharges(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var net int64
	for _, charge := range charges {
		if charge.RunID == n.RunID && charge.NodeID == n.NodeID {
			net += charge.AmountMicro
		}
	}
	if net <= 0 {
		t.Fatalf("execution start succeeded but settled charge = %d, want paid execution", net)
	}
}
