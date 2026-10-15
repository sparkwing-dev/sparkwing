package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeForClaimResponseRestoresOnlyRunnerFence(t *testing.T) {
	raw := &store.Node{
		RunID: "run", NodeID: "node", ClaimedBy: "holder", ClaimWorkerID: "helper",
		ClaimExecutorKind: "private-claim-kind", ClaimReservationID: "private-claim-reservation",
		CoordinatorID: "private-coordinator", ClaimGeneration: 7, ClaimMembershipID: "membership",
		ExecutorKind: "agent", ExecutorID: "private-executor",
		ReservationID: "reservation",
		ExecutionAttempts: []store.ExecutionAttempt{{
			Attempt: 1, ClaimGeneration: 7, CoordinatorID: "private-attempt-coordinator", MembershipID: "private-attempt-membership",
			ExecutorID: "private-attempt-executor", HolderID: "private-attempt-holder", ReservationID: "private-attempt-reservation",
		}},
	}

	got := nodeForClaimResponse(raw)
	if got.ClaimedBy != "holder" || got.ClaimGeneration != 7 ||
		got.ClaimMembershipID != "membership" || got.ReservationID != "reservation" {
		t.Fatalf("claim response lost runner fence: %+v", got)
	}
	if got.ExecutorName != "helper" {
		t.Fatalf("claim response executor_name = %q, want helper", got.ExecutorName)
	}
	if got.ClaimWorkerID != "" || got.ClaimExecutorKind != "" || got.ClaimReservationID != "" ||
		got.CoordinatorID != "" || got.ExecutorID != "" {
		t.Fatalf("claim response exposed unrelated internal identity: %+v", got)
	}
	if attempt := got.ExecutionAttempts[0]; attempt.ClaimGeneration != 0 || attempt.CoordinatorID != "" ||
		attempt.MembershipID != "" || attempt.ExecutorID != "" || attempt.HolderID != "" || attempt.ReservationID != "" {
		t.Fatalf("claim response attempt exposed internal identity: %+v", attempt)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatalf("claim response leaked a private sentinel: %s", encoded)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"claimed_by": "holder", "claim_generation": float64(7),
		"claim_membership_id": "membership", "reservation_id": "reservation",
	} {
		if object[key] != want {
			t.Fatalf("claim response %s = %#v, want %#v: %s", key, object[key], want, encoded)
		}
	}
	for _, key := range []string{
		"claim_worker_id", "claim_executor_kind", "claim_reservation_id", "coordinator_id",
		"executor_id", "claim_token_prefix", "token_prefix", "principal",
	} {
		if _, ok := object[key]; ok {
			t.Fatalf("claim response exposed %q: %s", key, encoded)
		}
	}
	attempts := object["execution_attempts"].([]any)
	attemptObject := attempts[0].(map[string]any)
	for _, key := range []string{
		"claim_generation", "coordinator_id", "membership_id", "executor_id", "holder_id",
		"reservation_id", "token_prefix", "claim_token_prefix",
	} {
		if _, ok := attemptObject[key]; ok {
			t.Fatalf("claim response attempt exposed %q: %s", key, encoded)
		}
	}
}
