package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestHostedCheck_RefusesADependencyCycle(t *testing.T) {
	snap := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{
		{ID: "a", Deps: []string{"c"}},
		{ID: "b", Deps: []string{"a"}},
		{ID: "c", Deps: []string{"b"}},
	}}
	err := hostedCheck(snap)
	if err == nil || !strings.Contains(err.Error(), "dependency cycle a -> c -> b -> a") {
		t.Fatalf("err = %v, want the cycle named", err)
	}
	if err := hostedCheck(nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "a"}, {ID: "a"}}}); err == nil {
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
	snap := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "a", Deps: []string{"b"}}, {ID: "b", Deps: []string{"a"}}}}
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
	for name, snap := range map[string]nodeproto.PlanDoc{
		"runs_on":     {Nodes: []nodeproto.PlanNode{{ID: "a", Modifiers: &nodeproto.PlanModifiers{RunsOn: []string{"gpu"}}}}},
		"prefers":     {Nodes: []nodeproto.PlanNode{{ID: "a", Modifiers: &nodeproto.PlanModifiers{Prefers: []string{"fast"}}}}},
		"when_runner": {Nodes: []nodeproto.PlanNode{{ID: "a", Modifiers: &nodeproto.PlanModifiers{WhenRunner: []string{"linux"}}}}},
		"requires":    {Requires: []string{"arm64"}, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
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
	for name, snap := range map[string]nodeproto.PlanDoc{
		"plan_concurrency":        {PlanConc: &nodeproto.PlanConc{}, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
		"plan_concurrency_groups": {PlanConcs: []nodeproto.PlanConc{{}}, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
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
	snap := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "a"}, {ID: "b", Deps: []string{"a"}}}}
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

type usageRunner struct{}

func (usageRunner) RunNode(context.Context, runner.Request) runner.Result {
	return runner.Result{Outcome: sparkwing.Success, Usage: &runner.ResourceUsage{CPUTime: 3e9, MaxRSSBytes: 64 << 20, Wall: 4e9}}
}

func TestHostedRunNode_RecordsTheNodeProcessUsage(t *testing.T) {
	st, err := teststore.Open(newInternalPaths(t).StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const runID = "run-usage"
	if err := st.CreateRun(t.Context(), store.Run{ID: runID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	snap := nodeproto.PlanDoc{Nodes: []nodeproto.PlanNode{{ID: "a"}}}
	if err := hostedAdmit(t.Context(), st, runID, snap); err != nil {
		t.Fatal(err)
	}
	hostedRunNode(t.Context(), st, usageRunner{}, hostedRun{}, runID, "a")
	row, err := st.GetNode(t.Context(), runID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if row.CPUNanos != 3e9 || row.MaxRSSBytes != 64<<20 {
		t.Errorf("row usage cpu=%d rss=%d, want the runner's measurement", row.CPUNanos, row.MaxRSSBytes)
	}
}

func TestHostedCheck_RefusesAdmissionSettingsItDoesNotAdmit(t *testing.T) {
	for name, snap := range map[string]nodeproto.PlanDoc{
		"priority":        {Priority: 5, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
		"admission_class": {AdmissionClass: "batch", Nodes: []nodeproto.PlanNode{{ID: "a"}}},
		"claim_wait":      {ClaimWaitMS: 1000, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
		"plan_resources":  {Resources: &nodeproto.PlanResources{}, Nodes: []nodeproto.PlanNode{{ID: "a"}}},
		"node_cores":      {Nodes: []nodeproto.PlanNode{{ID: "a", Modifiers: &nodeproto.PlanModifiers{ResCores: 2}}}},
		"node_memory":     {Nodes: []nodeproto.PlanNode{{ID: "a", Modifiers: &nodeproto.PlanModifiers{ResMemoryBytes: 1 << 30}}}},
	} {
		t.Run(name, func(t *testing.T) {
			err := hostedCheck(snap)
			if err == nil || !strings.Contains(err.Error(), "does not") {
				t.Fatalf("err = %v, want a refusal", err)
			}
		})
	}
}

func TestRunHosted_ASetupFailureAfterTheRunRowFinishesIt(t *testing.T) {
	SetTestStoreWedgeBudget(t, 0, errors.New("store wedge budget not-a-duration"))
	paths := newInternalPaths(t)
	res, err := runHosted(t.Context(), hostedRun{
		Describe: []byte(`[{"name":"p","args":[]}]`),
		Pipeline: "p",
		Paths:    paths,
		Logger:   slog.New(slog.DiscardHandler),
	})
	if err == nil || res.RunID == "" {
		t.Fatalf("res=%+v err=%v, want a setup failure after the run row exists", res, err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	run, err := st.GetRun(t.Context(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || run.FinishedAt == nil {
		t.Errorf("run row status=%q finished=%v, want failed and finished", run.Status, run.FinishedAt)
	}
}
