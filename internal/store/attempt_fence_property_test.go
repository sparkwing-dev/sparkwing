package store

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func sourceAttempt[T ~int | ~int64](attempt T) *runspec.AttemptOrdinal {
	ordinal := runspec.AttemptOrdinal(attempt)
	return &ordinal
}

func TestHolderReportsAffectOnlyTheirSourceAttemptProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 3.0s measured in the short class; the full suite runs it")
	}
	random := rand.New(rand.NewPCG(761, 109))
	for range 32 {
		tenant, _, actor := liveHolderFixture(t)
		retries := 2 + random.IntN(5)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET execution_json=?`, []byte(fmt.Sprintf(`{"retry":{"set":true,"value":%d}}`, retries)))
		ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
		var previous nodeTransition
		for ordinal := int64(3); ordinal < int64(3+retries); ordinal++ {
			before, err := tenant.ReadNode(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			change := nodeTransition{Ref: ref, To: runstate.NodeRunning, Actor: actor, Cause: runstate.CauseRetry, SourceAttempt: sourceAttempt(ordinal), Extra: transitionExtra{Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: "verify", MaskedError: "same failure"}}}
			applied, err := tenant.TransitionNode(t.Context(), change)
			if err != nil || applied.Replayed || int64(applied.Attempt) != ordinal+1 {
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
			for _, fence := range []*runspec.AttemptOrdinal{nil, sourceAttempt(-1), sourceAttempt(ordinal), sourceAttempt(ordinal + 2 + random.Int64N(100))} {
				stale := nodeTransition{Ref: ref, To: runstate.NodeSuccess, Actor: actor, Cause: runstate.CauseReport, SourceAttempt: fence}
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
		finish := nodeTransition{Ref: ref, To: runstate.NodeSuccess, Actor: actor, Cause: runstate.CauseReport, SourceAttempt: sourceAttempt(int64(3 + retries))}
		for replay := range 2 {
			applied, err := tenant.TransitionNode(t.Context(), finish)
			if err != nil || applied.Replayed != (replay == 1) || int64(applied.Attempt) != int64(3+retries) {
				t.Fatalf("finish %d: %+v, %v", replay, applied, err)
			}
		}
	}
}

func TestSourceAttemptFencesUnstartedAndResumedClaims(t *testing.T) {
	for _, ordinal := range []int64{0, 3} {
		for _, cause := range []runstate.TransitionCause{runstate.CauseStart, runstate.CauseReport, runstate.CauseRelease} {
			t.Run(fmt.Sprintf("%s/%d", cause, ordinal), func(t *testing.T) {
				tenant, _, actor := liveHolderFixture(t)
				rewriteSchemaExec(t, tenant.s.db, `DELETE FROM node_attempts`)
				rewriteSchemaExec(t, tenant.s.db, `UPDATE nodes SET status='pending',started_at=NULL,attempt=?`, ordinal)
				to := nodePending
				if cause == runstate.CauseStart {
					to = runstate.NodeRunning
				} else if cause == runstate.CauseReport {
					to = runstate.NodeFailed
				}
				change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: to, Actor: actor, Cause: cause}
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
				if cause == runstate.CauseStart {
					want++
				}
				if err != nil || int64(applied.Attempt) != want {
					t.Fatalf("explicit source attempt: %+v, %v", applied, err)
				}
				replayed, err := tenant.TransitionNode(t.Context(), change)
				if err != nil || !replayed.Replayed || int64(replayed.Attempt) != want {
					t.Fatalf("source attempt replay: %+v, %v", replayed, err)
				}
			})
		}
	}
}

func TestOwnerAndReaperDoNotSelectExecutionAttempts(t *testing.T) {
	tenant, _, reaper := stateFixture(t)
	change := nodeTransition{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, To: nodePending, Cause: runstate.CauseLeaseLapse, Actor: reaper, SourceAttempt: sourceAttempt(3)}
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
	change = nodeTransition{Ref: change.Ref, To: runstate.NodeCancelled, Cause: causeCancel, Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "owner"}}, SourceAttempt: sourceAttempt(3)}
	if _, err := tenant.TransitionNode(t.Context(), change); err == nil {
		t.Fatal("operator selected an execution attempt")
	}
	change.SourceAttempt = nil
	if _, err := tenant.TransitionNode(t.Context(), change); err != nil {
		t.Fatal(err)
	}
}
