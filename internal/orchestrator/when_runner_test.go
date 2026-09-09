package orchestrator_test

import (
	"context"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type whenRunnerSkipPipe struct{ sparkwing.Base }

func (whenRunnerSkipPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
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
	defer func() { _ = st.Close() }()

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
	defer func() { _ = st.Close() }()

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
	defer func() { _ = st.Close() }()

	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Success) {
		t.Errorf("outcome = %q, want %q (comma-OR should match local)", nodes[0].Outcome, sparkwing.Success)
	}
}

type whenRunnerPlatformPipe struct{ sparkwing.Base }

func (whenRunnerPlatformPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	for id, label := range map[string]string{
		"current-os":   "os=" + runtime.GOOS,
		"current-arch": "arch=" + runtime.GOARCH,
		"foreign-os":   "os=other-" + runtime.GOOS,
		"foreign-arch": "arch=other-" + runtime.GOARCH,
	} {
		sparkwing.Job(plan, id, func(context.Context) error { return nil }).WhenRunner(label)
	}
	return nil
}

func init() {
	register("when-runner-platform", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &whenRunnerPlatformPipe{} })
}

func TestRun_WhenRunnerUsesCurrentLocalPlatform(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "when-runner-platform"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "success" {
		t.Fatalf("run status = %q: %v", res.Status, res.Error)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"current-os": string(sparkwing.Success), "current-arch": string(sparkwing.Success),
		"foreign-os": string(sparkwing.Skipped), "foreign-arch": string(sparkwing.Skipped),
	}
	if len(nodes) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(nodes), len(want))
	}
	for _, node := range nodes {
		if node.Outcome != want[node.NodeID] {
			t.Errorf("%s outcome = %q, want %q", node.NodeID, node.Outcome, want[node.NodeID])
		}
	}
}
