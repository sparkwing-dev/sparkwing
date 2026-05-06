package orchestrator_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// stepRangeRanFlags is the canary set every stepRangePipe test
// uses: a/b/c steps that each tick a counter so tests can assert
// which actually executed.
type stepRangeRanFlags struct {
	a, b, c atomic.Bool
}

var stepRangeFlags stepRangeRanFlags

type stepRangeIntegJob struct{ sparkwing.Base }

func (stepRangeIntegJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	a := w.Step("fetch", func(ctx context.Context) error { stepRangeFlags.a.Store(true); return nil })
	b := w.Step("compile", func(ctx context.Context) error { stepRangeFlags.b.Store(true); return nil }).Needs(a)
	w.Step("publish", func(ctx context.Context) error { stepRangeFlags.c.Store(true); return nil }).Needs(b)
	return nil, nil
}

type stepRangeIntegPipe struct{ sparkwing.Base }

func (stepRangeIntegPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", stepRangeIntegJob{})
	return nil
}

// IMP-007: full slice through Options.StartAt -> validation -> ctx
// install -> RunWork skip. If any seam is wrong the run still
// reports success but the wrong steps execute.
func TestRunLocal_StartAtSkipsUpstreamSteps(t *testing.T) {
	register("orch-imp007-ok", func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangeIntegPipe{} })
	stepRangeFlags = stepRangeRanFlags{}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-imp007-ok",
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

// Unknown --start-at fails the run BEFORE any node dispatches; the
// run-level Error carries the Levenshtein-suggesting message.
func TestRunLocal_StartAtUnknownFailsRunBeforeDispatch(t *testing.T) {
	register("orch-imp007-typo", func() sparkwing.Pipeline[sparkwing.NoInputs] { return stepRangeIntegPipe{} })
	stepRangeFlags = stepRangeRanFlags{}

	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-imp007-typo",
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
	// Crucially: NO step ran.
	if stepRangeFlags.a.Load() || stepRangeFlags.b.Load() || stepRangeFlags.c.Load() {
		t.Errorf("no step should have executed when validation fails up front")
	}
}
