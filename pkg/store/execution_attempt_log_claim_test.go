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
	live, err := s.NodeExecutionAttemptIsLive(ctx, n.RunID, n.NodeID, fence, 1, time.Now())
	if err != nil || live {
		t.Fatalf("finished attempt live = %v, %v, want false", live, err)
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

func TestTriggerExecutionAttemptBelongsToLiveClaimOutlivesTheAttempt(t *testing.T) {
	ctx := context.Background()
	s := storetest.Open(t)
	identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_runner"}
	if err := s.CreateTrigger(ctx, store.Trigger{
		ID: "run-trigger-closing", Pipeline: "p", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fence := store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq}
	fenced := store.WithTriggerClaimFence(ctx, fence)
	if err := s.CreateRun(fenced, store.Run{
		ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(fenced, store.Node{RunID: trigger.ID, NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(fenced, trigger.ID, "build"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "build", identity,
		store.ExecutionStart{ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishNodeExecutionAttempt(fenced, trigger.ID, "build", identity,
		store.ExecutionAttemptFinish{
			ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1, Outcome: "success",
		}); err != nil {
		t.Fatal(err)
	}
	live, err := s.TriggerExecutionAttemptIsLive(ctx, trigger.ID, "build", fence, 1, time.Now())
	if err != nil || live {
		t.Fatalf("finished trigger attempt live = %v, %v, want false", live, err)
	}
	owned, err := s.TriggerExecutionAttemptBelongsToLiveClaim(ctx, trigger.ID, "build", fence, 1, time.Now())
	if err != nil || !owned {
		t.Fatalf("finished trigger attempt owned by the live claim = %v, %v, want true", owned, err)
	}
	stale := store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq + 1}
	owned, err = s.TriggerExecutionAttemptBelongsToLiveClaim(ctx, trigger.ID, "build", stale, 1, time.Now())
	if err != nil || owned {
		t.Fatalf("stale trigger generation owned = %v, %v, want false", owned, err)
	}
}
