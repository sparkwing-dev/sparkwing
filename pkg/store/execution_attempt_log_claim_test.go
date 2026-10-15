package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestNodeExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	createRetryRunAndReadyNode(t, s, "run-closing-lines", 0)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	n := claimNode(t, s, "run-closing-lines", identity, "agent:a:1")
	fence := store.NodeClaimFence{
		Claimant: identity, HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	start := store.ExecutionStart{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID, ReservationID: n.ReservationID,
		ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1,
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, n.RunID, n.NodeID, identity, start); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeExecutionAttempt(ctx, n.RunID, n.NodeID, identity, store.ExecutionAttemptFinish{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID, ReservationID: n.ReservationID,
		ClaimGeneration: n.ClaimGeneration, AttemptOrdinal: 1, Outcome: "success",
	}); err != nil {
		t.Fatal(err)
	}
	owned, err := s.NodeExecutionAttemptBelongsToLiveClaim(ctx, n.RunID, n.NodeID, fence, 1, time.Now())
	if err != nil || !owned {
		t.Fatalf("finished attempt owned by the live claim = %v, %v, want true", owned, err)
	}
	for name, other := range map[string]store.NodeClaimFence{
		"other principal": {
			Claimant: store.ClaimIdentity{Principal: "intruder", TokenPrefix: "swr_intruder"},
			HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		},
		"other holder": {
			Claimant: identity, HolderID: "agent:b:1", MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
		},
		"stale generation": {
			Claimant: identity, HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
			ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration - 1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			owned, err := s.NodeExecutionAttemptBelongsToLiveClaim(ctx, n.RunID, n.NodeID, other, 1, time.Now())
			if err != nil || owned {
				t.Fatalf("owned = %v, %v, want false", owned, err)
			}
		})
	}
	if _, err := s.DB().ExecContext(ctx, storetest.Rebind(s,
		`UPDATE nodes SET lease_expires_at = ? WHERE run_id = ? AND node_id = ?`),
		time.Now().Add(-time.Second).UnixNano(), n.RunID, n.NodeID); err != nil {
		t.Fatal(err)
	}
	owned, err = s.NodeExecutionAttemptBelongsToLiveClaim(ctx, n.RunID, n.NodeID, fence, 1, time.Now())
	if err != nil || owned {
		t.Fatalf("lapsed claim owned = %v, %v, want false", owned, err)
	}
}
