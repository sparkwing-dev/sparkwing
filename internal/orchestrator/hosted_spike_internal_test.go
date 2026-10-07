package orchestrator

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestHostedCheck_RefusesADependencyCycle(t *testing.T) {
	snap := planSnapshot{Nodes: []snapshotNode{
		{ID: "a", Deps: []string{"c"}},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"b"}},
	}}
	err := hostedCheck(snap)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle a -> c -> b -> a") {
		t.Fatalf("err = %v, want the cycle named", err)
	}
	if err := hostedCheck(planSnapshot{Nodes: []snapshotNode{{ID: "a"}, {ID: "a"}}}); err == nil {
		t.Fatal("a plan holding one node twice was admitted")
	}
}

type countingRunner struct{ calls int }

func (r *countingRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.calls++
	return runner.Result{Outcome: sparkwing.Success}
}

func TestHostedSchedule_AStalledPlanFailsItsPendingNodes(t *testing.T) {
	st, err := teststore.Open(newInternalPaths(t).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	snap := planSnapshot{Nodes: []snapshotNode{{ID: "a", Deps: []string{"b"}}, {ID: "b", Deps: []string{"a"}}}}
	lr := &countingRunner{}
	cfg := hostedRun{Logger: slog.New(slog.DiscardHandler)}

	order, results := hostedSchedule(t.Context(), st, lr, cfg, "run-stall", snap)
	if len(order) != 0 || lr.calls != 0 {
		t.Fatalf("started %v (%d runner calls) from a plan where nothing is ready", order, lr.calls)
	}
	for _, id := range []string{"a", "b"} {
		if got := results[id].Outcome; got != sparkwing.Failed {
			t.Errorf("node %s outcome = %q, want failed", id, got)
		}
	}
}

func TestHostedCheck_RefusesPlacementItDoesNotEnforce(t *testing.T) {
	for name, snap := range map[string]planSnapshot{
		"runs_on":     {Nodes: []snapshotNode{{ID: "a", Modifiers: &snapshotModifiers{RunsOn: []string{"gpu"}}}}},
		"prefers":     {Nodes: []snapshotNode{{ID: "a", Modifiers: &snapshotModifiers{Prefers: []string{"fast"}}}}},
		"when_runner": {Nodes: []snapshotNode{{ID: "a", Modifiers: &snapshotModifiers{WhenRunner: []string{"linux"}}}}},
		"requires":    {Requires: []string{"arm64"}, Nodes: []snapshotNode{{ID: "a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			err := hostedCheck(snap)
			if err == nil || !strings.Contains(err.Error(), "does not") {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
}

func TestHostedCheck_RefusesPlanLevelConcurrency(t *testing.T) {
	for name, snap := range map[string]planSnapshot{
		"plan_concurrency":        {PlanConc: &snapshotConc{}, Nodes: []snapshotNode{{ID: "a"}}},
		"plan_concurrency_groups": {PlanConcs: []snapshotConc{{}}, Nodes: []snapshotNode{{ID: "a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			err := hostedCheck(snap)
			if err == nil || !strings.Contains(err.Error(), "plan-level concurrency") {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
}

type cancellingRunner struct{ cancel context.CancelFunc }

func (r cancellingRunner) RunNode(context.Context, runner.Request) runner.Result {
	r.cancel()
	return runner.Result{Outcome: sparkwing.Cancelled}
}

func TestHostedFinalize_CancellationFinishesEveryNodeRow(t *testing.T) {
	st, err := teststore.Open(newInternalPaths(t).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const runID = "run-cancel"
	if err := st.CreateRun(t.Context(), store.Run{ID: runID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	snap := planSnapshot{Nodes: []snapshotNode{{ID: "a"}, {ID: "b", Deps: []string{"a"}}}}
	if err := hostedAdmit(t.Context(), st, runID, snap); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cfg := hostedRun{Logger: slog.New(slog.DiscardHandler)}

	_, results := hostedSchedule(ctx, st, cancellingRunner{cancel: cancel}, cfg, runID, snap)
	hostedFinalize(ctx, st, cfg.Logger, runID, snap, results)

	for _, id := range []string{"a", "b"} {
		row, err := st.GetNode(t.Context(), runID, id)
		if err != nil {
			t.Fatal(err)
		}
		if !runner.NodeTerminal(row) || row.Outcome != string(sparkwing.Cancelled) {
			t.Errorf("node %s row status=%q outcome=%q, want done/cancelled", id, row.Status, row.Outcome)
		}
		if got := results[id].Outcome; got != sparkwing.Cancelled {
			t.Errorf("node %s result = %q, want cancelled", id, got)
		}
	}
}
