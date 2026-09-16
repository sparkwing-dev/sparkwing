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
			claimant.TokenPrefix, "runner_limit", time.Now())
	}()
	if err := waitForPostgresEligibilityWaiter(ctx, s); err != nil {
		_ = blocker.Rollback()
		t.Fatal(err)
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
	if net < 0 {
		t.Fatalf("settled charge = %d, want cancellation to mint no credits", net)
	}
}

func waitForPostgresEligibilityWaiter(ctx context.Context, s *store.Store) error {
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for waitCtx.Err() == nil {
		var count int
		err := s.DB().QueryRowContext(waitCtx, `
WITH key AS (SELECT hashtext($1)::bigint AS value)
SELECT COUNT(*)
  FROM pg_locks, key
 WHERE locktype = 'advisory' AND NOT granted
   AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
   AND classid::bigint = ((key.value >> 32) & 4294967295)
   AND objid::bigint = (key.value & 4294967295)`, "sparkwing/executor-eligibility").Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
	}
	return waitCtx.Err()
}
