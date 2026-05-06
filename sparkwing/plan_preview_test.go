package sparkwing_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// IMP-013: PreviewPlan must NOT execute step bodies. The canary
// counters in this file's test pipelines must remain zero after
// every call.
var previewExecCounter atomic.Int64

func nopStep(ctx context.Context) error {
	previewExecCounter.Add(1)
	return nil
}

// --- Single-step pipeline: every step would_run, no skips. ---

type previewSinglePipe struct{ sparkwing.Base }

func (previewSinglePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "only", sparkwing.JobFn(nopStep))
	return nil
}

func TestPreviewPlan_SingleStepWouldRun(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp013-single",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewSinglePipe{} })
	reg, _ := sparkwing.Lookup("imp013-single")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp013-single"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewExecCounter.Store(0)
	preview, err := sparkwing.PreviewPlan(plan, "imp013-single", nil, sparkwing.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if previewExecCounter.Load() != 0 {
		t.Fatalf("step body executed during preview (counter = %d) -- preview must be pure", previewExecCounter.Load())
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].ID != "only" {
		t.Fatalf("expected one node 'only', got %+v", preview.Nodes)
	}
	if preview.Nodes[0].Decision != "would_run" {
		t.Errorf("expected would_run, got %q", preview.Nodes[0].Decision)
	}
}

// --- SkipIf-always-true: step shows skip_reason=user_skipif. ---

type previewSkipPipe struct{ sparkwing.Base }

type previewSkipJob struct{ sparkwing.Base }

func (previewSkipJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.Step("a", nopStep)
	w.Step("b", nopStep).SkipIf(func(context.Context) bool { return true })
	return w
}

func (previewSkipPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", previewSkipJob{})
	return nil
}

func TestPreviewPlan_UserSkipIfReportedAsUserSkipIf(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp013-skipif",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewSkipPipe{} })
	reg, _ := sparkwing.Lookup("imp013-skipif")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp013-skipif"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewExecCounter.Store(0)
	preview, err := sparkwing.PreviewPlan(plan, "imp013-skipif", nil, sparkwing.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if previewExecCounter.Load() != 0 {
		t.Fatalf("step body executed (counter = %d)", previewExecCounter.Load())
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Work == nil {
		t.Fatalf("expected one node with Work, got %+v", preview.Nodes)
	}
	steps := preview.Nodes[0].Work.Steps
	want := map[string]string{"a": "would_run", "b": "would_skip"}
	for _, s := range steps {
		if s.Decision != want[s.ID] {
			t.Errorf("step %q decision: got %q, want %q", s.ID, s.Decision, want[s.ID])
		}
	}
	for _, s := range steps {
		if s.ID == "b" && s.SkipReason != "user_skipif" {
			t.Errorf("step b skip_reason: got %q, want user_skipif", s.SkipReason)
		}
	}
}

// --- --start-at on second step: first step shows skip_reason=range_skip. ---

type previewRangePipe struct{ sparkwing.Base }

type previewRangeJob struct{ sparkwing.Base }

func (previewRangeJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	a := w.Step("a", nopStep)
	w.Step("b", nopStep).Needs(a)
	return w
}

func (previewRangePipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", previewRangeJob{})
	return nil
}

func TestPreviewPlan_StartAtRangeSkipReported(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp013-range",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewRangePipe{} })
	reg, _ := sparkwing.Lookup("imp013-range")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp013-range"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewExecCounter.Store(0)
	preview, err := sparkwing.PreviewPlan(plan, "imp013-range", nil, sparkwing.PreviewOptions{StartAt: "b"})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if previewExecCounter.Load() != 0 {
		t.Fatalf("step body executed during preview (counter = %d)", previewExecCounter.Load())
	}
	if preview.StartAt != "b" {
		t.Errorf("StartAt echo: got %q, want b", preview.StartAt)
	}
	steps := preview.Nodes[0].Work.Steps
	for _, s := range steps {
		if s.ID == "a" {
			if s.Decision != "would_skip" || s.SkipReason != "range_skip" {
				t.Errorf("step a: got decision=%q reason=%q, want would_skip / range_skip", s.Decision, s.SkipReason)
			}
			if s.SkipDetail == "" {
				t.Errorf("step a should carry a SkipDetail describing the bound")
			}
		}
		if s.ID == "b" && s.Decision != "would_run" {
			t.Errorf("step b decision: got %q, want would_run", s.Decision)
		}
	}
}

// --- Dynamic fan-out: SpawnNodeForEach reports cardinality=unresolved. ---

type previewFanOutJob struct{ sparkwing.Base }

func (previewFanOutJob) Work() *sparkwing.Work {
	w := sparkwing.NewWork()
	w.Step("seed", nopStep)
	// Empty-slice generator is fine for plan-time -- we never run it.
	w.SpawnNodeForEach([]string{}, func(s string) (string, sparkwing.Workable) {
		return "child-" + s, sparkwing.JobFn(nopStep)
	})
	return w
}

type previewFanOutPipe struct{ sparkwing.Base }

func (previewFanOutPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "fanout", previewFanOutJob{})
	return nil
}

func TestPreviewPlan_DynamicFanOutCardinalityUnresolved(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("imp013-fanout",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewFanOutPipe{} })
	reg, _ := sparkwing.Lookup("imp013-fanout")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp013-fanout"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewExecCounter.Store(0)
	preview, err := sparkwing.PreviewPlan(plan, "imp013-fanout", nil, sparkwing.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if previewExecCounter.Load() != 0 {
		t.Fatalf("step body executed (counter = %d)", previewExecCounter.Load())
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Work == nil {
		t.Fatalf("expected one node with Work")
	}
	if len(preview.Nodes[0].Work.SpawnEach) != 1 {
		t.Fatalf("expected one SpawnEach generator, got %d", len(preview.Nodes[0].Work.SpawnEach))
	}
	gen := preview.Nodes[0].Work.SpawnEach[0]
	if gen.Cardinality != "unresolved" {
		t.Errorf("cardinality: got %q, want unresolved", gen.Cardinality)
	}
}

// --- Resolved args + lint warnings round-trip onto the wire shape. ---

type previewArgsInputs struct {
	Tag string `flag:"tag" desc:"a tag"`
}

type previewArgsPipe struct{ sparkwing.Base }

func (previewArgsPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ previewArgsInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "only", sparkwing.JobFn(nopStep))
	return nil
}

func TestPreviewPlan_ResolvedArgsRoundtrip(t *testing.T) {
	sparkwing.Register[previewArgsInputs]("imp013-args",
		func() sparkwing.Pipeline[previewArgsInputs] { return previewArgsPipe{} })
	reg, _ := sparkwing.Lookup("imp013-args")
	args := map[string]string{"tag": "v1.2.3"}
	plan, err := reg.Invoke(context.Background(), args, sparkwing.RunContext{Pipeline: "imp013-args"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewExecCounter.Store(0)
	preview, err := sparkwing.PreviewPlan(plan, "imp013-args", args, sparkwing.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if previewExecCounter.Load() != 0 {
		t.Fatalf("step body executed (counter = %d)", previewExecCounter.Load())
	}
	if got := preview.ResolvedArgs["tag"]; got != "v1.2.3" {
		t.Errorf("ResolvedArgs[tag]: got %q, want v1.2.3", got)
	}
}
