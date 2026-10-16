package store_test

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestAPendingRunCarriesNoFinishTime(t *testing.T) {
	s := storetest.Open(t)
	tenant, plan, err := storePlanSealRequest(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), plan.RunID)
	if err != nil || run.State != store.RunPending || run.FinishedAt != nil {
		t.Fatalf("pending run: %+v, %v", run, err)
	}
}

func TestATerminalRunAlwaysCarriesAFinishTime(t *testing.T) {
	for _, status := range []store.RunState{store.RunSuccess, store.RunCancelled, store.RunFailed} {
		t.Run(string(status), func(t *testing.T) {
			var tenant *store.Tenant
			var s *store.Store
			var finish store.FinishRunRequest
			if status == store.RunFailed {
				f := newStoreRecoveryFixtureAt(t, true, storetest.New(t))
				tenant, s = f.tenant, f.s
				if _, err := tenant.TransitionRun(t.Context(), store.RunTransition{RunID: f.ref.RunID, Actor: f.owner, To: store.RunRunning, Cause: store.RunStart}); err != nil {
					t.Fatal(err)
				}
				if _, err := tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.holder, To: store.NodeFailed, Cause: store.CauseReport, SourceAttempt: acceptanceAttempt(1), Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeFailed}}}); err != nil {
					t.Fatal(err)
				}
				finish = store.FinishRunRequest{Unit: f.owner.Owner, Transition: store.RunTransition{RunID: f.ref.RunID, Actor: f.owner, To: status, Cause: store.RunFinish}}
			} else {
				s = storetest.Open(t)
				tenant, finish = runCompletionFixture(t, s)
				if status == store.RunCancelled {
					run, err := tenant.ReadRun(t.Context(), finish.Unit.RunID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tenant.TransitionRun(t.Context(), store.RunTransition{RunID: run.ID, To: store.RunCancelled, Cause: store.RunCancel, Actor: store.Actor{Kind: store.ActorOperator, Authority: run.Request.AuthorityRef}}); err != nil {
						t.Fatal(err)
					}
				}
				finish.Transition.To = status
			}
			before, err := s.TestOnlyDatabaseTime(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(t.Context(), finish.Unit.RunID)
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
			after, err := tenant.ReadRun(t.Context(), finish.Unit.RunID)
			if err != nil || !reflect.DeepEqual(run, after) {
				t.Fatalf("replay changed terminal state: %+v, %v", after, err)
			}
		})
	}
}
