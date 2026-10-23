package store_test

import (
	"math/rand/v2"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func runCompletionFixture(t *testing.T, s *store.Store) (*store.Tenant, runstate.FinishRunRequest) {
	t.Helper()
	tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(_ *store.IntakeRequest, doc *nodeproto.PlanDoc) { doc.Jobs = []nodeproto.JobSpec{} })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
		t.Fatal(err)
	}
	return tenant, runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Cause: runstate.RunFinish, Actor: plan.Actor}}
}

func TestRunCompletionHasOneCommittedWinner(t *testing.T) {
	target := storetest.NewSQLite(t)
	first, second := target.Open(t), target.Open(t)
	tenant, finish := runCompletionFixture(t, first)
	other, err := second.ForTeam(t.Context(), tenant.Team())
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := range 8 {
		wg.Go(func() {
			<-ready
			applied, err := []*store.Tenant{tenant, other}[i%2].FinishRunAndUnit(t.Context(), finish)
			if err != nil {
				t.Error(err)
				return
			}
			if !applied.Replayed {
				wins.Add(1)
			}
		})
	}
	close(ready)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("committed winners=%d, want one", wins.Load())
	}
	before, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
	if err != nil || before.FinishedAt == nil || before.State != runstate.RunSuccess {
		t.Fatalf("winning run: %+v, %v", before, err)
	}
	for _, state := range []runstate.RunState{runstate.RunSuccess, runstate.RunFailed, runstate.RunCancelled} {
		retry := finish
		retry.Transition.MaskedError = string(state) + " late report"
		if _, err := other.FinishRunAndUnit(t.Context(), retry); err == nil {
			t.Fatalf("different terminal report %s accepted", state)
		}
	}
	after, err := tenant.ReadRun(t.Context(), string(finish.Unit.RunID))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("late report rewrote terminal run: %+v, %v", after, err)
	}
}

func TestRunCompletionAuthorityProperty(t *testing.T) {
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
	tenant, finish := runCompletionFixture(t, s)
	foreign := tenantFor(t, s, "foreign")
	before := readMutationSnapshot(t, tenant, s, string(finish.Unit.RunID))

	for _, cause := range []runstate.RunCause{runstate.RunStart, runstate.RunCancel, runstate.RunPublish, runstate.RunOwnerLost} {
		invalid := finish
		invalid.Transition.Cause = cause
		if _, err := tenant.FinishRunAndUnit(t.Context(), invalid); err == nil {
			t.Fatalf("non-completion cause %s accepted", cause)
		}
		if after := readMutationSnapshot(t, tenant, s, string(finish.Unit.RunID)); !reflect.DeepEqual(before, after) {
			t.Fatal("invalid completion changed persisted state")
		}
	}

	rng := rand.New(rand.NewPCG(83, 29))
	for i := range 40 {
		forged := finish
		target := tenant
		suffix := string(rune('a' + rng.IntN(26)))
		switch i % 8 {
		case 0:
			forged.Transition.Actor.CapabilityPrefix += suffix
		case 1:
			forged.Transition.Actor.Authority.ID += suffix
		case 2:
			forged.Transition.Actor.Owner.Generation += runspec.ClaimGeneration(1 + rng.Int64N(100))
		case 3:
			forged.Unit.Generation += runspec.ClaimGeneration(1 + rng.Int64N(100))
		case 4:
			forged.Unit.Holder += suffix
			forged.Transition.Actor.Owner = forged.Unit
		case 5:
			forged.Unit.RunID += runspec.RunID(suffix)
			forged.Transition.Actor.Owner = forged.Unit
		case 6:
			target = foreign
		case 7:
			forged.Transition.Actor.Kind = runstate.ActorHolder
		}
		if _, err := target.FinishRunAndUnit(t.Context(), forged); err == nil {
			t.Fatalf("forged owner accepted: %+v", forged)
		}
		if after := readMutationSnapshot(t, tenant, s, string(finish.Unit.RunID)); !reflect.DeepEqual(before, after) {
			t.Fatal("refused owner changed persisted state")
		}
	}
	if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
		t.Fatal(err)
	}
}

func TestRunCompletionFailureRollsBackOwnerAndVerdict(t *testing.T) {
	for _, table := range []string{"events", "audit_events"} {
		t.Run(table, func(t *testing.T) {
			s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
			tenant, finish := runCompletionFixture(t, s)
			before := readMutationSnapshot(t, tenant, s, string(finish.Unit.RunID))
			if _, err := s.DB().ExecContext(t.Context(), `CREATE TRIGGER refuse_completion_event BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT,'event refused'); END`); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err == nil {
				t.Fatal("completion ignored failed event")
			}
			if after := readMutationSnapshot(t, tenant, s, string(finish.Unit.RunID)); !reflect.DeepEqual(before, after) {
				t.Fatal("failed completion committed partial state")
			}
			if _, err := s.DB().ExecContext(t.Context(), `DROP TRIGGER refuse_completion_event`); err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil {
				t.Fatal(err)
			}
		})
	}
}
