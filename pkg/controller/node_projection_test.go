package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/executionpolicy"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func requireExactJSONKeys(t *testing.T, got map[string]any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("JSON keys = %v, want exactly %v", got, want)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("JSON keys = %v, missing %q", got, key)
		}
	}
}

func TestNodeForClaimResponseRestoresOnlyRunnerFence(t *testing.T) {
	raw := &store.Node{
		RunID: "run", NodeID: "node", ClaimedBy: "holder", ClaimWorkerID: "helper",
		ClaimExecutorKind: "private-claim-kind", ClaimReservationID: "private-claim-reservation",
		CoordinatorID: "private-coordinator", ClaimGeneration: 7, ClaimMembershipID: "membership",
		ExecutorKind: "agent", ExecutorID: "private-executor", RequiredCoordinatorID: "private-required-coordinator",
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
		got.CoordinatorID != "" || got.ExecutorID != "" || got.RequiredCoordinatorID != "" {
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
		"executor_id", "required_coordinator_id", "avoid_coordinator_id", "avoid_executor_kind",
		"avoid_executor_id", "claim_token_prefix", "token_prefix", "principal",
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

func TestExecutorClaimPreparationForResponseContainsOnlyReservationInputs(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	raw := &store.ExecutorClaimPreparation{
		Summary: store.ExecutorSchedulingSummary{
			RunID: "run", NodeID: "node", ResourceDigest: "sha256:digest", Slots: 1,
			Resources:             store.ExecutorResource{Cores: 2, MemoryBytes: 1024},
			HardCapabilities:      []string{"private-hard-capability"},
			PreferredCapabilities: []string{"private-preferred-capability"}, RunPriority: 91,
			RequiredCoordinatorID: "private-coordinator",
			RequiredLocation:      "private-location",
		},
		Membership: store.ExecutorMembershipSnapshot{
			MembershipID: "membership", WorkerID: "helper", Eligible: true, MaxConcurrent: 3,
			Kind: "private-kind", RegisteredBasePriority: 11, EffectivePriority: 22,
			HighestEligiblePriority: 33, ActiveSlots: 2,
		},
		OfferDeadline: &deadline,
	}

	got := executorClaimPreparationForResponse(raw, executionpolicy.ClaimBinding{})
	if got.Summary.RunID != "run" || got.Summary.NodeID != "node" || got.Summary.ResourceDigest != "sha256:digest" ||
		got.Membership.MembershipID != "membership" || got.Membership.WorkerID != "helper" || got.Membership.EffectivePriority != 22 {
		t.Fatalf("preparation lost executor admission contract: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-") {
		t.Fatalf("preparation leaked an unused sentinel: %s", encoded)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	requireExactJSONKeys(t, object, "summary", "membership", "offer_deadline")
	summary := object["summary"].(map[string]any)
	requireExactJSONKeys(t, summary, "run_id", "node_id", "resources", "resource_digest", "slots")
	requireExactJSONKeys(t, summary["resources"].(map[string]any), "cores", "memory_bytes")
	membership := object["membership"].(map[string]any)
	requireExactJSONKeys(t, membership, "membership_id", "worker_id", "eligible", "effective_priority", "max_concurrent")
	if raw.Summary.RequiredCoordinatorID != "private-coordinator" {
		t.Fatal("projection mutated source preparation")
	}
}
