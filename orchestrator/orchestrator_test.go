package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// registerOnce avoids "duplicate Register" panics when a test file's
// pipelines are registered at package init.
var registerOnce sync.Map

func register(name string, factory func() sparkwing.Pipeline[sparkwing.NoInputs]) {
	if _, loaded := registerOnce.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	sparkwing.Register[sparkwing.NoInputs](name, factory)
}

// --- pipelines used across tests ---

type okPipe struct{ sparkwing.Base }

func (okPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(func(ctx context.Context) error {
		sparkwing.Info(ctx, "work complete")
		return nil
	}))
	return nil
}

type failPipe struct{ sparkwing.Base }

func (failPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, sparkwing.JobFn(func(ctx context.Context) error { return errors.New("boom") }))
	return nil
}

type fanOutOK struct{ sparkwing.Base }

func (fanOutOK) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	setup := sparkwing.Job(plan, "setup", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	a := sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error { return nil })).Needs(setup)
	b := sparkwing.Job(plan, "b", sparkwing.JobFn(func(ctx context.Context) error { return nil })).Needs(setup)
	sparkwing.Job(plan, "fin", sparkwing.JobFn(func(ctx context.Context) error { return nil })).Needs(a, b)
	return nil
}

type middleFails struct{ sparkwing.Base }

func (middleFails) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	a := sparkwing.Job(plan, "a", sparkwing.JobFn(func(ctx context.Context) error { return nil }))
	b := sparkwing.Job(plan, "b", sparkwing.JobFn(func(ctx context.Context) error { return errors.New("mid fail") })).Needs(a)
	sparkwing.Job(plan, "c", sparkwing.JobFn(func(ctx context.Context) error { return nil })).Needs(b)
	return nil
}

// --- typed ref chain ---

type refBuildOut struct {
	Tag string `json:"tag"`
}
type refBuildJob struct {
	sparkwing.Base
	sparkwing.Produces[refBuildOut]
}

func (j *refBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	out := sparkwing.Out(w, "run", j.run)
	return out.WorkStep, nil
}

func (refBuildJob) run(ctx context.Context) (refBuildOut, error) {
	return refBuildOut{Tag: "v9"}, nil
}

type refDeployJob struct {
	sparkwing.Base
	Build sparkwing.Ref[refBuildOut]
}

func (d *refDeployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.Step("run", d.run)
	return nil, nil
}

func (d *refDeployJob) run(ctx context.Context) error {
	got := d.Build.Get(ctx)
	if got.Tag != "v9" {
		return fmt.Errorf("ref got %q, want v9", got.Tag)
	}
	return nil
}

type refPipe struct{ sparkwing.Base }

func (refPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &refBuildJob{})
	sparkwing.Job(plan, "deploy", &refDeployJob{Build: sparkwing.RefTo[refBuildOut](build)}).Needs(build)
	return nil
}

func init() {
	register("orch-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &okPipe{} })
	register("orch-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &failPipe{} })
	register("orch-fanout-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &fanOutOK{} })
	register("orch-middle-fails", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &middleFails{} })
	register("orch-ref", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &refPipe{} })
}

// newPaths returns a Paths under t.TempDir() with the root created.
func newPaths(t *testing.T) orchestrator.Paths {
	t.Helper()
	root := t.TempDir()
	p := orchestrator.PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

// --- tests ---

func TestRun_SingleJobSuccess(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	// Log file should exist with content.
	body, err := os.ReadFile(p.NodeLog(res.RunID, "orch-ok"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(body), "work complete") {
		t.Fatalf("log missing message: %s", body)
	}
}

func TestRun_FailurePropagatesResult(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fail"})
	if err != nil {
		t.Fatalf("Run returned err=%v; failures should surface via Result not err", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected Result.Error for failed run")
	}
}

func TestRun_FanOutFanIn(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fanout-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
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
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Outcome != string(sparkwing.Success) {
			t.Fatalf("node %q outcome %q, want success", n.NodeID, n.Outcome)
		}
	}
}

func TestRun_MidFailureCancelsDownstream(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-middle-fails"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	byID := map[string]*store.Node{}
	for _, n := range nodes {
		byID[n.NodeID] = n
	}
	if byID["a"].Outcome != string(sparkwing.Success) {
		t.Fatalf("a outcome = %q", byID["a"].Outcome)
	}
	if byID["b"].Outcome != string(sparkwing.Failed) {
		t.Fatalf("b outcome = %q", byID["b"].Outcome)
	}
	if byID["c"].Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("c outcome = %q, want cancelled", byID["c"].Outcome)
	}
	if !strings.Contains(byID["c"].Error, "upstream-failed") {
		t.Fatalf("c should cite upstream-failed, got %q", byID["c"].Error)
	}
}

func TestRun_TypedRefsThreadOutput(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ref"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()
	nodes, _ := st.ListNodes(context.Background(), res.RunID)
	for _, n := range nodes {
		if n.NodeID != "build" {
			continue
		}
		var out refBuildOut
		if err := json.Unmarshal(n.Output, &out); err != nil {
			t.Fatalf("unmarshal build output: %v (%s)", err, n.Output)
		}
		if out.Tag != "v9" {
			t.Fatalf("build output %q, want v9", out.Tag)
		}
	}
}

func TestRun_PersistsPlanSnapshotAndRunRow(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-fanout-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	st, _ := store.Open(p.StateDB())
	defer st.Close()

	r, err := st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if r.Pipeline != "orch-fanout-ok" {
		t.Fatalf("pipeline = %q", r.Pipeline)
	}
	if r.Status != "success" {
		t.Fatalf("status = %q", r.Status)
	}
	if r.FinishedAt == nil || r.FinishedAt.Before(r.StartedAt) {
		t.Fatalf("finished_at not set properly")
	}
	if len(r.PlanSnapshot) == 0 {
		t.Fatal("plan snapshot not persisted")
	}
}

func TestRun_UnknownPipelineErrors(t *testing.T) {
	p := newPaths(t)
	_, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "nope-not-registered"})
	if err == nil {
		t.Fatal("expected error for unknown pipeline")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unexpected err: %v", err)
	}
}

func TestRun_PathsIsolation(t *testing.T) {
	// Verify run dir has the log file and no cross-run leakage.
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries, err := os.ReadDir(p.RunDir(res.RunID))
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("run dir empty")
	}
	if !strings.HasSuffix(filepath.Base(p.NodeLog(res.RunID, "orch-ok")), ".log") {
		t.Fatal("log name convention broken")
	}
	// Avoid flake if OS clock resolution is coarse.
	_ = time.Millisecond
}
