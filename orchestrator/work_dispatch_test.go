package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestWorkDispatch_* tests Work-shape nodes: registered via sw.Job,
// dispatched through the in-process runner's Work-execution path
// (RunWork), emitting structured step_start/step_end events and
// producing typed output via the designated ResultStep.

// --- Work-shape pipelines ---

// multiStepWork emits step boundary events and threads typed output
// between steps via TypedStep.Get + TypedStep.Needs.
type multiStepWorkJob struct {
	sparkwing.Base
	sparkwing.Produces[workOut]
}

func (multiStepWorkJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	prep := w.Step("prep", func(ctx context.Context) error {
		sparkwing.Info(ctx, "prep ran")
		return nil
	})
	tags := sparkwing.Out(w, "compute-tags", func(ctx context.Context) (workOut, error) {
		return workOut{Tag: "vv"}, nil
	})
	tags.Needs(prep)
	w.Step("publish", func(ctx context.Context) error {
		got := tags.Get(ctx)
		if got.Tag != "vv" {
			return fmt.Errorf("unexpected tag %q", got.Tag)
		}
		sparkwing.Info(ctx, "published tag=%s", got.Tag)
		return nil
	}).Needs(tags.WorkStep)
	w.SetResult(tags.WorkStep)
	return w
}

type workOut struct {
	Tag string `json:"tag"`
}

type workMultiPipe struct{ sparkwing.Base }

func (workMultiPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "multi", multiStepWorkJob{})
	return nil
}

// failingWorkJob fails its terminal step; the node should report
// failed and the run should fail.
type failingWorkJob struct{ sparkwing.Base }

func (failingWorkJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.Step("ok", func(ctx context.Context) error { return nil })
	w.Step("nope", func(ctx context.Context) error { return errors.New("intentional") })
	return w
}

type workFailPipe struct{ sparkwing.Base }

func (workFailPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "fail-work", failingWorkJob{})
	return nil
}

// jobFnPipe registers a single-closure Job via JobFn through sw.Job.
type jobFnPipe struct{ sparkwing.Base }

var jobFnRan atomic.Bool

func (jobFnPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "via-add", sparkwing.JobFn(func(ctx context.Context) error {
		jobFnRan.Store(true)
		sparkwing.Info(ctx, "JobFn ran via sw.Job")
		return nil
	}))
	return nil
}

func init() {
	register("workdisp-multi", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &workMultiPipe{} })
	register("workdisp-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &workFailPipe{} })
	register("workdisp-jobfn", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &jobFnPipe{} })
}

// --- tests ---

func TestWorkDispatch_MultiStepWorkSucceeds(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "workdisp-multi"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	body, err := os.ReadFile(p.NodeLog(res.RunID, "multi"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logText := string(body)
	for _, want := range []string{`"event":"step_start"`, `"event":"step_end"`, `"step":"prep"`, `"step":"compute-tags"`, `"step":"publish"`, `published tag=vv`} {
		if !strings.Contains(logText, want) {
			t.Errorf("log missing %q\nfull log:\n%s", want, logText)
		}
	}
}

func TestWorkDispatch_TypedResultPersistedAsNodeOutput(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "workdisp-multi"})
	if err != nil || res.Status != "success" {
		t.Fatalf("Run: status=%q err=%v rerr=%v", res.Status, err, res.Error)
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "multi" {
		t.Fatalf("expected 1 multi node, got %+v", nodes)
	}
	var out workOut
	if err := json.Unmarshal(nodes[0].Output, &out); err != nil {
		t.Fatalf("unmarshal output: %v (%s)", err, nodes[0].Output)
	}
	if out.Tag != "vv" {
		t.Fatalf("Node.Output Tag = %q, want vv -- ResultStep output should be persisted as the node's typed output", out.Tag)
	}
}

func TestWorkDispatch_FailingStepFailsTheNode(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "workdisp-fail"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	// Run-level error is summary-only ("nodes failed: [fail-work]");
	// the underlying step error is on the per-node row, so assert
	// against the store rather than res.Error.
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	nodes, err := st.ListNodes(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeID != "fail-work" {
		t.Fatalf("expected one fail-work node, got %+v", nodes)
	}
	if nodes[0].Outcome != string(sparkwing.Failed) {
		t.Fatalf("node outcome = %q, want failed", nodes[0].Outcome)
	}
	if !strings.Contains(nodes[0].Error, "intentional") {
		t.Fatalf("node error should cite the step error 'intentional', got %q", nodes[0].Error)
	}
	if !strings.Contains(nodes[0].Error, "nope") {
		t.Fatalf("node error should cite the failing step id 'nope', got %q", nodes[0].Error)
	}
}

func TestWorkDispatch_JobFnRunsViaWorkPath(t *testing.T) {
	jobFnRan.Store(false)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "workdisp-jobfn"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if !jobFnRan.Load() {
		t.Fatal("JobFn closure did not execute")
	}
	body, _ := os.ReadFile(p.NodeLog(res.RunID, "via-add"))
	if !strings.Contains(string(body), `"event":"step_start"`) {
		t.Errorf("expected step_start in JobFn-via-Add log; got:\n%s", body)
	}
}
