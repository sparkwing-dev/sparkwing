package store_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestRunCompletionRecordsDryRun(t *testing.T) {
	s := storetest.OpenSQLite(t, store.WithPolicies(sealStorePolicies))
	tenant, plan, err := storePlanSealRequestWith(t.Context(), s, func(input *store.IntakeRequest, doc *nodeproto.PlanDoc) {
		input.Request.Options.DryRun = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "dry_run"}}
		doc.Jobs = []nodeproto.JobSpec{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.AcceptPlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.TransitionRun(t.Context(), runstate.RunTransition{RunID: runspec.RunID(plan.RunID), Actor: plan.Actor, To: runstate.RunRunning, Cause: runstate.RunStart}); err != nil {
		t.Fatal(err)
	}
	finish := runstate.FinishRunRequest{Unit: plan.Actor.Owner, Transition: runstate.RunCompletion{RunID: runspec.RunID(plan.RunID), Cause: runstate.RunFinish, Actor: plan.Actor}}
	applied, err := tenant.FinishRunAndUnit(t.Context(), finish)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Outcome != "dry_run" {
		t.Fatalf("dry run completed as %q", applied.Outcome)
	}
	row, err := tenant.ReadRun(t.Context(), plan.RunID)
	if err != nil || row.State != "dry_run" || row.FinishedAt == nil {
		t.Fatalf("persisted dry run: %+v, %v", row, err)
	}
	if replay, err := tenant.FinishRunAndUnit(t.Context(), finish); err != nil || !replay.Replayed || replay.Outcome != "dry_run" {
		t.Fatalf("replayed dry run: %+v, %v", replay, err)
	}
	effects, err := s.AsOperator().DueRunEffects(t.Context(), 10)
	if err != nil || len(effects) != 4 {
		t.Fatalf("dry-run completion effects: %+v, %v", effects, err)
	}
	for _, effect := range effects {
		if effect.State != "dry_run" || effect.Phase != "terminal" {
			t.Fatalf("dry run produced nonterminal effect: %+v", effect)
		}
	}
}

func TestDryRunKeepsFailureAndCancellationVerdicts(t *testing.T) {
	for _, test := range []struct {
		name      string
		cancelled bool
		outcome   string
		want      string
	}{
		{name: "preview", outcome: "success", want: "dry_run"},
		{name: "failure", outcome: "failed", want: "failed"},
		{name: "cancellation", cancelled: true, outcome: "success", want: "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := store.Decide(store.ScheduleSnapshot{DryRun: true, CancelRequested: test.cancelled, Nodes: []store.ScheduleNode{{ID: "job", Status: "done", Outcome: test.outcome}}})
			if decision.Outcome != test.want {
				t.Fatalf("dry-run verdict %q, want %q", decision.Outcome, test.want)
			}
		})
	}
}
