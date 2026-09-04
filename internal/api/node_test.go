package api

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPublicNodeIsIdempotent(t *testing.T) {
	raw := &store.Node{
		RunID: "run", NodeID: "node", ClaimedBy: "private-holder",
		ClaimWorkerID: "desktop", ExecutorKind: "agent", ClaimGeneration: 9,
	}
	first := PublicNode(raw)
	second := PublicNode(first)
	if !first.Claimed || !second.Claimed || second.ExecutorName != "desktop" {
		t.Fatalf("projected node lost public attribution: first=%+v second=%+v", first, second)
	}
	if second.ClaimedBy != "" || second.ClaimGeneration != 0 {
		t.Fatalf("projected node exposed claim identity: %+v", second)
	}
	if raw.Claimed || raw.ExecutorName != "" || raw.ClaimedBy != "private-holder" {
		t.Fatalf("projection mutated source: %+v", raw)
	}
}

func TestPublicNodeScrubsInternalExecutionIdentity(t *testing.T) {
	avoidUntil := time.Now().Add(time.Minute)
	raw := &store.Node{
		RunID: "run", NodeID: "node", ClaimedBy: "private-holder",
		ClaimWorkerID: "desktop", ClaimExecutorKind: "agent", ClaimReservationID: "claim-reservation",
		CoordinatorID: "coordinator", ClaimGeneration: 9, ClaimMembershipID: "membership",
		ExecutorKind: "agent", ExecutorID: "executor", RequiredCoordinatorID: "required-coordinator",
		ReservationID: "reservation", AvoidCoordinatorID: "avoid-coordinator",
		AvoidExecutorKind: "gateway", AvoidExecutorID: "avoid-executor", AvoidUntil: &avoidUntil,
		ExecutionAttempts: []store.ExecutionAttempt{{
			RunID: "run", NodeID: "node", Attempt: 1, ClaimGeneration: 9,
			CoordinatorID: "coordinator", MembershipID: "membership", ExecutorID: "executor",
			HolderID: "private-holder", ReservationID: "reservation",
		}},
	}

	got := PublicNode(raw)
	if !got.Claimed || got.ExecutorName != "desktop" {
		t.Fatalf("projected node lost public execution attribution: %+v", got)
	}
	if got.ClaimedBy != "" || got.ClaimWorkerID != "" || got.ClaimExecutorKind != "" ||
		got.ClaimReservationID != "" || got.CoordinatorID != "" || got.ClaimGeneration != 0 ||
		got.ClaimMembershipID != "" || got.ExecutorID != "" || got.RequiredCoordinatorID != "" ||
		got.ReservationID != "" || got.AvoidCoordinatorID != "" || got.AvoidExecutorKind != "" ||
		got.AvoidExecutorID != "" || got.AvoidUntil != nil {
		t.Fatalf("projected node exposed internal identity: %+v", got)
	}
	if len(got.ExecutionAttempts) != 1 {
		t.Fatalf("execution attempts = %d, want 1", len(got.ExecutionAttempts))
	}
	attempt := got.ExecutionAttempts[0]
	if attempt.ClaimGeneration != 0 || attempt.CoordinatorID != "" || attempt.MembershipID != "" ||
		attempt.ExecutorID != "" || attempt.HolderID != "" || attempt.ReservationID != "" {
		t.Fatalf("projected attempt exposed internal identity: %+v", attempt)
	}
	got.ExecutionAttempts[0].Outcome = "mutated"
	if raw.ExecutionAttempts[0].Outcome != "" {
		t.Fatal("projection retained the source attempt slice")
	}
}
