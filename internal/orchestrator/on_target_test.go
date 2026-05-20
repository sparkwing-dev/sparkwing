package orchestrator_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	onTargetProdRan   atomic.Int32
	onTargetDevRan    atomic.Int32
	onTargetCommonRan atomic.Int32
)

type onTargetPipe struct{ sparkwing.Base }

func (onTargetPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "common", func(context.Context) error {
		onTargetCommonRan.Add(1)
		return nil
	})
	sparkwing.Job(plan, "deploy-prod", func(context.Context) error {
		onTargetProdRan.Add(1)
		return nil
	}).OnTarget("prod")
	sparkwing.Job(plan, "deploy-dev", func(context.Context) error {
		onTargetDevRan.Add(1)
		return nil
	}).OnTarget("dev")
	return nil
}

type onTargetInheritPipe struct{ sparkwing.Base }

var onTargetInheritUpstreamRan atomic.Int32

func (onTargetInheritPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	up := sparkwing.Job(plan, "build", func(context.Context) error {
		onTargetInheritUpstreamRan.Add(1)
		return nil
	})
	sparkwing.Job(plan, "release", func(context.Context) error {
		return nil
	}).OnTarget("prod").Needs(up)
	return nil
}

type onTargetUndeclaredPipe struct{ sparkwing.Base }

func (onTargetUndeclaredPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "x", func(context.Context) error { return nil }).OnTarget("typo")
	return nil
}

type onTargetNoTargetsPipe struct{ sparkwing.Base }

func (onTargetNoTargetsPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "x", func(context.Context) error { return nil }).OnTarget("prod")
	return nil
}

type onTargetAccessorPipe struct{ sparkwing.Base }

var (
	onTargetPlanTarget atomic.Value // string
	onTargetJobTarget  atomic.Value // string
)

func (onTargetAccessorPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	onTargetPlanTarget.Store(sparkwing.Target(ctx))
	sparkwing.Job(plan, "show", func(ctx context.Context) error {
		onTargetJobTarget.Store(sparkwing.Target(ctx))
		return nil
	})
	return nil
}

func init() {
	register("on-target-multi", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onTargetPipe{} })
	register("on-target-inherit", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onTargetInheritPipe{} })
	register("on-target-undeclared", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onTargetUndeclaredPipe{} })
	register("on-target-no-targets", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onTargetNoTargetsPipe{} })
	register("on-target-accessor", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &onTargetAccessorPipe{} })
}

func multiTargetYAML() *pipelines.Pipeline {
	return &pipelines.Pipeline{
		Name: "on-target-multi",
		Targets: map[string]pipelines.Target{
			"prod": {},
			"dev":  {},
		},
	}
}

func TestRun_OnTargetSkipsNonMatchingJobs(t *testing.T) {
	onTargetProdRan.Store(0)
	onTargetDevRan.Store(0)
	onTargetCommonRan.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "on-target-multi",
		Target:       "prod",
		PipelineYAML: multiTargetYAML(),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if onTargetProdRan.Load() != 1 {
		t.Errorf("prod job runs once, got %d", onTargetProdRan.Load())
	}
	if onTargetDevRan.Load() != 0 {
		t.Errorf("dev job should be skipped, got %d", onTargetDevRan.Load())
	}
	if onTargetCommonRan.Load() != 1 {
		t.Errorf("common universal job runs, got %d", onTargetCommonRan.Load())
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
	for _, n := range nodes {
		if n.NodeID == "deploy-dev" && n.Outcome != string(sparkwing.Skipped) {
			t.Errorf("deploy-dev outcome = %q, want skipped", n.Outcome)
		}
	}
}

func TestRun_OnTargetEmptyTargetSkipsAllNonUniversal(t *testing.T) {
	onTargetProdRan.Store(0)
	onTargetDevRan.Store(0)
	onTargetCommonRan.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "on-target-multi",
		PipelineYAML: multiTargetYAML(),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if onTargetProdRan.Load() != 0 || onTargetDevRan.Load() != 0 {
		t.Errorf("only universal jobs should run, got prod=%d dev=%d",
			onTargetProdRan.Load(), onTargetDevRan.Load())
	}
	if onTargetCommonRan.Load() != 1 {
		t.Errorf("common universal job should run, got %d", onTargetCommonRan.Load())
	}
}

func TestRun_OnTargetInferredUpstreamSkips(t *testing.T) {
	onTargetInheritUpstreamRan.Store(0)
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "on-target-inherit",
		Target:   "dev",
		PipelineYAML: &pipelines.Pipeline{
			Name: "on-target-inherit",
			Targets: map[string]pipelines.Target{
				"prod": {},
				"dev":  {},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if got := onTargetInheritUpstreamRan.Load(); got != 0 {
		t.Errorf("build should be skipped via inferred OnTarget from release, got %d runs", got)
	}
}

func TestRun_OnTargetUndeclaredTargetFailsAtValidation(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "on-target-undeclared",
		Target:   "prod",
		PipelineYAML: &pipelines.Pipeline{
			Name: "on-target-undeclared",
			Targets: map[string]pipelines.Target{
				"prod": {},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal returned process error: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "OnTarget(\"typo\")") {
		t.Errorf("expected undeclared-target error, got %v", res.Error)
	}
}

func TestRun_OnTargetOnNoTargetsPipelineFails(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "on-target-no-targets",
		PipelineYAML: &pipelines.Pipeline{Name: "on-target-no-targets"},
	})
	if err != nil {
		t.Fatalf("RunLocal returned process error: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "pipeline declares no targets") {
		t.Errorf("expected no-targets validation error, got %v", res.Error)
	}
}

func TestRun_TargetAccessorVisibleInPlanAndStepBodies(t *testing.T) {
	onTargetPlanTarget.Store("")
	onTargetJobTarget.Store("")
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "on-target-accessor",
		Target:   "staging",
		PipelineYAML: &pipelines.Pipeline{
			Name: "on-target-accessor",
			Targets: map[string]pipelines.Target{
				"staging": {},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if got, _ := onTargetPlanTarget.Load().(string); got != "staging" {
		t.Errorf("Plan saw Target = %q, want staging", got)
	}
	if got, _ := onTargetJobTarget.Load().(string); got != "staging" {
		t.Errorf("step body saw Target = %q, want staging", got)
	}
}

func TestRun_PushTriggerTargetDefaultsForFor(t *testing.T) {
	onTargetProdRan.Store(0)
	onTargetDevRan.Store(0)
	onTargetCommonRan.Store(0)
	p := newPaths(t)
	yaml := multiTargetYAML()
	yaml.On.Push = &pipelines.PushTrigger{Branches: []string{"main"}, Target: "prod"}
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "on-target-multi",
		Trigger:      sparkwing.TriggerInfo{Source: "push"},
		PipelineYAML: yaml,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if onTargetProdRan.Load() != 1 {
		t.Errorf("push trigger should default target=prod and run prod job, got %d", onTargetProdRan.Load())
	}
	if onTargetDevRan.Load() != 0 {
		t.Errorf("dev job should not run, got %d", onTargetDevRan.Load())
	}
}

func TestRun_CLIForOverridesTriggerTarget(t *testing.T) {
	onTargetProdRan.Store(0)
	onTargetDevRan.Store(0)
	onTargetCommonRan.Store(0)
	p := newPaths(t)
	yaml := multiTargetYAML()
	yaml.On.Push = &pipelines.PushTrigger{Branches: []string{"main"}, Target: "prod"}
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "on-target-multi",
		Target:       "dev",
		Trigger:      sparkwing.TriggerInfo{Source: "push"},
		PipelineYAML: yaml,
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}
	if onTargetDevRan.Load() != 1 {
		t.Errorf("CLI --for=dev should win, dev job got %d runs", onTargetDevRan.Load())
	}
	if onTargetProdRan.Load() != 0 {
		t.Errorf("prod job should not run, got %d", onTargetProdRan.Load())
	}
}
