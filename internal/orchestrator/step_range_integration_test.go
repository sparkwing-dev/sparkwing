package orchestrator_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type stepRangeRanFlags struct {
	a, b, c atomic.Bool
}

var stepRangeFlags stepRangeRanFlags

type stepRangeIntegJob struct{ sparkwing.Base }

func (stepRangeIntegJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	a := sparkwing.Step(w, "fetch", func(ctx context.Context) error { stepRangeFlags.a.Store(true); return nil })
	b := sparkwing.Step(w, "compile", func(ctx context.Context) error { stepRangeFlags.b.Store(true); return nil }).Needs(a)
	sparkwing.Step(w, "publish", func(ctx context.Context) error { stepRangeFlags.c.Store(true); return nil }).Needs(b)
	return nil, nil
}

type stepRangeIntegPipe struct{ sparkwing.Base }

func (stepRangeIntegPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", stepRangeIntegJob{})
	return nil
}

func TestRunLocal_StartAtSkipsUpstreamSteps(t *testing.T) {
	register("orch-step-range-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangeIntegPipe{} })
	stepRangeFlags = stepRangeRanFlags{}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-step-range-ok",
		StartAt:  "compile",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success", res.Status)
	}
	if stepRangeFlags.a.Load() {
		t.Errorf("fetch should be skipped (--start-at=compile)")
	}
	if !stepRangeFlags.b.Load() || !stepRangeFlags.c.Load() {
		t.Errorf("compile + publish should run; got compile=%v publish=%v",
			stepRangeFlags.b.Load(), stepRangeFlags.c.Load())
	}
}

func TestStepRange_PreviewMatchesPersistedExecution(t *testing.T) {
	const pipeline = "orch-step-range-preview-parity"
	register(pipeline, func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangeIntegPipe{} })
	reg, _ := sparkwing.Lookup(pipeline)
	plan, err := reg.Invoke(t.Context(), nil, sparkwing.RunContext{Pipeline: pipeline})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	preview, err := sparkwingruntime.PreviewPlan(plan, pipeline, nil, sparkwingruntime.PreviewOptions{
		StartAt: "compile",
		StopAt:  "compile",
	})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	wantPreview := map[string]struct {
		decision string
		reason   string
	}{
		"fetch":   {decision: "would_skip", reason: "range_skip"},
		"compile": {decision: "would_run"},
		"publish": {decision: "would_skip", reason: "range_skip"},
	}
	for _, step := range preview.Nodes[0].Work.Steps {
		want := wantPreview[step.ID]
		if step.Decision != want.decision || step.SkipReason != want.reason {
			t.Errorf("preview %s = %s/%s, want %s/%s", step.ID, step.Decision, step.SkipReason, want.decision, want.reason)
		}
		delete(wantPreview, step.ID)
	}
	if len(wantPreview) != 0 {
		t.Fatalf("missing preview steps: %v", wantPreview)
	}

	stepRangeFlags = stepRangeRanFlags{}
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(t.Context(), paths, orchestrator.Options{
		Pipeline: pipeline,
		StartAt:  "compile",
		StopAt:   "compile",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != "success" || stepRangeFlags.a.Load() || !stepRangeFlags.b.Load() || stepRangeFlags.c.Load() {
		t.Fatalf("execution status=%s fetch=%v compile=%v publish=%v", result.Status, stepRangeFlags.a.Load(), stepRangeFlags.b.Load(), stepRangeFlags.c.Load())
	}
	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = state.Close() }()
	steps, err := state.ListNodeSteps(t.Context(), result.RunID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	wantStatus := map[string]string{"fetch": store.StepSkipped, "compile": store.StepPassed, "publish": store.StepSkipped}
	for _, step := range steps {
		if want := wantStatus[step.StepID]; step.Status != want {
			t.Errorf("stored %s status = %s, want %s", step.StepID, step.Status, want)
		}
		delete(wantStatus, step.StepID)
	}
	if len(wantStatus) != 0 {
		t.Fatalf("missing stored steps: %v", wantStatus)
	}
}

func TestRunLocal_StartAtUnknownFailsRunBeforeDispatch(t *testing.T) {
	register("orch-step-range-typo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangeIntegPipe{} })
	stepRangeFlags = stepRangeRanFlags{}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-step-range-typo",
		StartAt:  "fetchh",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), `did you mean "fetch"`) {
		t.Errorf("Error missing Levenshtein suggestion, got: %v", res.Error)
	}
	if stepRangeFlags.a.Load() || stepRangeFlags.b.Load() || stepRangeFlags.c.Load() {
		t.Errorf("no step should have executed when validation fails up front")
	}
}
