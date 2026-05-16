package orchestrator_test

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type whenRunnerSkipPipe struct{ sparkwing.Base }

func (whenRunnerSkipPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	// The in-process runner advertises ["local"]; this job demands a
	// runner advertising "cloud-windows" and so should be skipped.
	preflight := sparkwing.Job(plan, "windows-only", func(ctx context.Context) error {
		return nil
	}).WhenRunner("cloud-windows")
	sparkwing.Job(plan, "downstream", func(ctx context.Context) error {
		return nil
	}).Needs(preflight)
	return nil
}

type whenRunnerLocalPipe struct{ sparkwing.Base }

func (whenRunnerLocalPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "local-only", func(ctx context.Context) error {
		return nil
	}).WhenRunner("local")
	return nil
}

type whenRunnerCommaOrPipe struct{ sparkwing.Base }

func (whenRunnerCommaOrPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	// Local in-process runner advertises ["local"]; the comma-OR
	// expression accepts either local or cloud-linux, so the job
	// should run.
	sparkwing.Job(plan, "preflight", func(ctx context.Context) error {
		return nil
	}).WhenRunner("local,cloud-linux")
	return nil
}

func init() {
	register("when-runner-skip", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &whenRunnerSkipPipe{} })
	register("when-runner-local", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &whenRunnerLocalPipe{} })
	register("when-runner-comma-or", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &whenRunnerCommaOrPipe{} })
}

func TestRun_WhenRunnerSkipsJobWhenRunnerCannotSatisfy(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "when-runner-skip"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success (downstream Needs treats skipped as satisfied)", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	pre := byID["windows-only"]
	if pre == nil {
		t.Fatalf("windows-only node missing; nodes=%v", nodes)
	}
	if pre.Outcome != string(sparkwing.Skipped) {
		t.Errorf("windows-only outcome = %q, want %q", pre.Outcome, sparkwing.Skipped)
	}
	down := byID["downstream"]
	if down == nil {
		t.Fatalf("downstream node missing")
	}
	if down.Outcome != string(sparkwing.Success) {
		t.Errorf("downstream outcome = %q, want %q (Needs(skipped-WhenRunner) should satisfy)", down.Outcome, sparkwing.Success)
	}
}

func TestRun_WhenRunnerLocalRunsOnLocalRunner(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "when-runner-local"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Errorf("outcome = %q, want %q", nodes[0].Outcome, sparkwing.Success)
	}
}

func TestRun_WhenRunnerCommaOrMatchesLocal(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "when-runner-comma-or"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Errorf("outcome = %q, want %q (comma-OR should match local)", nodes[0].Outcome, sparkwing.Success)
	}
}
