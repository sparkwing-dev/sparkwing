package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func sourceAttempt(attempt int64) *int64 { return &attempt }

func TestHolderReportsAffectOnlyTheirSourceAttemptProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(761, 109))
	for range 32 {
		tenant, _, actor := liveHolderFixture(t)
		retries := 2 + random.IntN(5)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=?`, []byte(fmt.Sprintf(`{"retry":{"set":true,"value":%d}}`, retries)))
		ref := NodeRef{"run", "job"}
		var previous NodeTransition
		for ordinal := int64(3); ordinal < int64(3+retries); ordinal++ {
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			change := NodeTransition{Ref: ref, To: NodeRunning, Actor: actor, Cause: CauseRetry, SourceAttempt: sourceAttempt(ordinal), Extra: TransitionExtra{Result: &NodeResult{Outcome: NodeFailed, Reason: "verify", MaskedError: "same failure"}}}
			applied, err := tenant.TransitionNode(t.Context(), change)
			if err != nil || applied.Replayed || applied.Attempt != ordinal+1 {
				t.Fatalf("retry %d: %+v, %v", ordinal, applied, err)
			}
			for range 1 + random.IntN(4) {
				replayed, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !replayed.Replayed || replayed.Attempt != applied.Attempt || replayed.StateSeq != applied.StateSeq {
					t.Fatalf("retry replay: %+v, %v", replayed, err)
				}
			}
			after, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || after.Generation != before.Generation || after.CapabilityPrefix != before.CapabilityPrefix || after.ClaimedBy != before.ClaimedBy || !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) {
				t.Fatalf("retry lost claim: %+v, %v", after, err)
			}
			for _, fence := range []*int64{nil, sourceAttempt(-1), sourceAttempt(ordinal), sourceAttempt(ordinal + 2 + random.Int64N(100))} {
				stale := NodeTransition{Ref: ref, To: NodeSuccess, Actor: actor, Cause: CauseReport, SourceAttempt: fence}
				if _, err := tenant.TransitionNode(t.Context(), stale); err == nil {
					t.Fatalf("incorrect source attempt %v changed attempt %d", fence, ordinal+1)
				}
			}
			if previous.SourceAttempt != nil {
				if _, err := tenant.TransitionNode(t.Context(), previous); err == nil {
					t.Fatal("superseded retry report changed a newer attempt")
				}
			}
			unchanged, err := tenant.ReadNode(t.Context(), ref)
			if err != nil || !reflect.DeepEqual(after, unchanged) {
				t.Fatalf("refused report mutated job: %+v -> %+v, %v", after, unchanged, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "events", int(applied.StateSeq))
			previous = change
		}
		finish := NodeTransition{Ref: ref, To: NodeSuccess, Actor: actor, Cause: CauseReport, SourceAttempt: sourceAttempt(int64(3 + retries))}
		for replay := range 2 {
			applied, err := tenant.TransitionNode(t.Context(), finish)
			if err != nil || applied.Replayed != (replay == 1) || applied.Attempt != int64(3+retries) {
				t.Fatalf("finish %d: %+v, %v", replay, applied, err)
			}
		}
	}
}

func TestSourceAttemptFencesUnstartedAndResumedClaims(t *testing.T) {
	for _, ordinal := range []int64{0, 3} {
		for _, cause := range []TransitionCause{CauseStart, CauseReport, CauseRelease} {
			t.Run(fmt.Sprintf("%s/%d", cause, ordinal), func(t *testing.T) {
				tenant, _, actor := liveHolderFixture(t)
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',started_at=NULL,attempt=?`, ordinal)
				to := NodePending
				if cause == CauseStart {
					to = NodeRunning
				} else if cause == CauseReport {
					to = NodeFailed
				}
				change := NodeTransition{Ref: NodeRef{"run", "job"}, To: to, Actor: actor, Cause: cause}
				if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
					t.Fatal("omitted source attempt accepted")
				}
				change.SourceAttempt = sourceAttempt(ordinal + 1)
				if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
					t.Fatal("future source attempt accepted")
				}
				change.SourceAttempt = sourceAttempt(ordinal)
				applied, err := tenant.TransitionNode(t.Context(), change)
				want := ordinal
				if cause == CauseStart {
					want++
				}
				if err != nil || applied.Attempt != want {
					t.Fatalf("explicit source attempt: %+v, %v", applied, err)
				}
				replayed, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !replayed.Replayed || replayed.Attempt != want {
					t.Fatalf("source attempt replay: %+v, %v", replayed, err)
				}
			})
		}
	}
}

func TestOwnerAndReaperDoNotSelectExecutionAttempts(t *testing.T) {
	tenant, _, reaper := stateFixture(t)
	change := NodeTransition{Ref: NodeRef{"run", "job"}, To: NodePending, Cause: CauseLeaseLapse, Actor: reaper, SourceAttempt: sourceAttempt(3)}
	if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("reaper accepted a caller-selected attempt")
	}
	change.SourceAttempt = nil
	if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	root := authorityRoot("owner", "a")
	root.Grants = []capability.Grant{capability.RunsControl}
	authorityPersist(t, tenant.s, root)
	change = NodeTransition{Ref: change.Ref, To: NodeCancelled, Cause: CauseCancel, Actor: Actor{Kind: ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}}, SourceAttempt: sourceAttempt(3)}
	if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("operator selected an execution attempt")
	}
	change.SourceAttempt = nil
	if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
}
