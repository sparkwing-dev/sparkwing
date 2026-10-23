package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestAPendingRunCarriesNoFinishTime(t *testing.T) {
	s := storetest.Open(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), plan.RunID)
	if err != nil || run.State != runstate.RunPending || run.FinishedAt != nil {
		t.Fatalf("pending run: %+v, %v", run, err)
	}
}

func TestATerminalRunAlwaysCarriesAFinishTime(t *testing.T) {
	for _, status := range []runstate.RunState{runstate.RunSuccess, runstate.RunCancelled, runstate.RunFailed} {
		t.Run(string(status), func(t *testing.T) {
			var tenant *store.Tenant
			var s *store.Store
			var finish runstate.FinishRunRequest
			if status == runstate.RunFailed {
				f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
				tenant, s = f.tenant, f.s
				if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: f.ref.RunID, Actor: f.owner, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.TransitionNode(t.Context(), store.ReportNode{Ref: f.ref, Actor: f.holder, SourceAttempt: acceptanceAttempt(1), Outcome: runstate.NodeFailed, Result: &runstate.NodeResult{Outcome: runstate.NodeFailed}}); err != nil {
					t.Fatal(err)
				}
				finish = runstate.FinishRunRequest{Unit: f.owner.Owner, Transition: runstate.RunCompletion{RunID: f.ref.RunID, Actor: f.owner, Cause: runstate.RunFinish}}
			} else {
				s = storetest.Open(t, store.WithPolicies(sealStorePolicies))
				tenant, finish = runCompletionFixture(t, s)
				if status == runstate.RunCancelled {
					run, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(run.ID), To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: runstate.Actor{Kind: runstate.ActorOperator, Authority: run.Request.AuthorityRef}}); err != nil {
						t.Fatal(err)
					}
				}

			}
			before, err := s.TestOnlyDatabaseTime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
			afterTime, clockErr := s.TestOnlyDatabaseTime(t.Context())
			if clockErr != nil {
				t.Fatal(clockErr)
			}
			if err != nil || run.State != status || run.FinishedAt == nil || run.FinishedAt.Before(before) || run.FinishedAt.After(afterTime) {
				t.Fatalf("terminal timestamp: %+v, %v", run, err)
			}
			if applied, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil || !applied.Replayed {
				t.Fatalf("finish replay: %+v, %v", applied, err)
			}
			after, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
			if err != nil || !reflect.DeepEqual(run, after) {
				t.Fatalf("replay changed terminal state: %+v, %v", after, err)
			}
		})
	}
}
