package store

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerCancellationProofKeepsIssuerAttemptAndFenceProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(431, 719))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, now, owner := reconcileFixture(t)
			owner.Holder, owner.Generation = owner.Owner.Holder, owner.Owner.Generation
			owner.Result = &capability.RecordedResult{Actor: owner.Authority, Method: "POST", Pattern: "/api/v1/runs/{run}/local-host/nodes/{job}/transition", RunID: "run", NodeID: "job", Generation: owner.Generation, Digest: strings.Repeat("a", 64)}
			change := NodeTransition{Ref: NodeRef{"run", "job"}, Actor: owner, To: NodeCancelled, Cause: CauseCancel, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeCancelled, Reason: fmt.Sprintf("cancel/%x", random.Uint64())}}}
			mutation := i % 8
			switch mutation {
			case 1:
				owner.Result.Attempt = int64(1 + random.IntN(100))
			case 2:
				owner.Result.NodeID += fmt.Sprint(random.Uint64())
			case 3:
				owner.Result.Generation++
			case 4:
				change.Actor.Owner.Generation++
			case 5:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET lease_expires_at=?`, now.UnixNano())
			case 6:
				rewriteSchemaExec(t, tenant.s.db, `UPDATE capabilities SET revoked_at=1 WHERE prefix='owner'`)
			case 7:
				owner.Result.Actor.ID += fmt.Sprint(random.Uint64())
			}
			before, err := tenant.ReadNode(t.Context(), change.Ref)
			if err != nil {
				t.Fatal(err)
			}
			applied, err := tenant.TransitionNode(t.Context(), change)
			if mutation == 0 {
				if err != nil || !applied.CancelRequested {
					t.Fatalf("owner proof used the holder attempt: %+v %v", applied, err)
				}
				replayed, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !replayed.Replayed || replayed.StateSeq != applied.StateSeq {
					t.Fatalf("cancellation replay: %+v %v", replayed, err)
				}
			} else if err == nil {
				t.Fatal("mismatched cancellation authority was accepted")
			}
			after, err := tenant.ReadNode(t.Context(), change.Ref)
			if err != nil || after.Status != before.Status || after.Attempt != before.Attempt || after.Generation != before.Generation || after.ClaimedBy != before.ClaimedBy || mutation != 0 && after.StateSeq != before.StateSeq {
				t.Fatalf("cancellation changed holder rights: %+v %v", after, err)
			}
		})
	}
}

func TestCancellationIntentReachesHolderRenewal(t *testing.T) {
	tenant, _, owner := reconcileFixture(t)
	ref := NodeRef{"run", "job"}
	_, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: owner, To: NodeCancelled, Cause: CauseCancel, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeCancelled, Reason: "user_cancelled"}}})
	if err != nil {
		t.Fatal(err)
	}
	holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", Holder: "runner", Generation: 2}
	beat, err := tenant.RenewNodeClaim(t.Context(), ref, holder)
	if err != nil || !beat.CancelRequested || beat.CancelReason != "user_cancelled" {
		t.Fatalf("cancellation intent: %+v, %v", beat, err)
	}
}

func TestCancellationIntentAndHolderResultStayIndependentProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(271, 521))
	for i := range 32 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tenant, _, owner := reconcileFixture(t)
			operator := authorityMember(t, tenant.s, "operator", RoleOwner)
			operator.Kind = ActorOperator
			ref := NodeRef{"run", "job"}
			reason := fmt.Sprintf("%s/%x", []string{"user_cancelled", "' OR 1=1 --", "雪\n\x00", ""}[random.IntN(4)], random.Uint64())
			runCancel := random.IntN(2) == 0
			cancel := func(reason string) {
				t.Helper()
				var err error
				if runCancel {
					_, err = tenant.TransitionRun(t.Context(), RunTransition{RunID: "run", Actor: operator, To: RunCancelled, Cause: RunCancel, Reason: reason})
				} else {
					_, err = tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: owner, To: NodeCancelled, Cause: CauseCancel, Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeCancelled, Reason: reason}}})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			cancel(reason)
			cancel("later cancellation")
			holder := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "claim"}, CapabilityPrefix: "claim", Holder: "runner", Generation: 2}
			beat, err := tenant.RenewNodeClaim(t.Context(), ref, holder)
			if err != nil || !beat.CancelRequested || beat.CancelReason != reason {
				t.Fatalf("holder intent changed: %+v, %v", beat, err)
			}
			if runCancel {
				beat, err = tenant.RenewOwner(t.Context(), owner)
				if err != nil || !beat.CancelRequested || beat.CancelReason != reason {
					t.Fatalf("owner intent changed: %+v, %v", beat, err)
				}
			}
			result := &NodeResult{Outcome: NodeCancelled, Reason: "executor_cancelled", MaskedError: "drained"}
			if _, err := tenant.TransitionNode(t.Context(), NodeTransition{Ref: ref, Actor: holder, To: NodeCancelled, Cause: CauseReport, SourceAttempt: sourceAttempt(3), Extra: TransitionExtra{Result: result}}); err != nil {
				t.Fatal(err)
			}
			row, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || row.Outcome != NodeCancelled || row.CancelReason != reason || row.CancelRequestedAt == nil {
				t.Fatalf("terminal result replaced intent: %+v, %v", row, err)
			}
			events, err := tenant.ReadStateEvents(t.Context(), "run", 0, 20)
			if err != nil || len(events) == 0 {
				t.Fatalf("events: %+v, %v", events, err)
			}
			var recorded NodeResult
			last := events[len(events)-1]
			if err := json.Unmarshal(last.Payload, &recorded); err != nil || last.Kind != "job_cancelled" || recorded.Reason != result.Reason || recorded.MaskedError != result.MaskedError {
				t.Fatalf("intent replaced holder result: %+v, %v", last, err)
			}
		})
	}
}
