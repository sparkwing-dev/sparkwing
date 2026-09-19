package sparkwingruntime_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

var previewSideEffects atomic.Int32

type previewSealPipe struct{ sparkwing.Base }

type previewSealJob struct{ sparkwing.Base }

// safety: the preview evaluates a step's SkipIf, not a node's, so the probe has
// to sit on a step to be reached at all.
func (previewSealJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "probe", func(context.Context) error { return nil }).
		SkipIf(func(ctx context.Context) bool {
			planguard.Guard(ctx, "preview.probe")
			previewSideEffects.Add(1)
			return false
		})
	return nil, nil
}

func (previewSealPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", previewSealJob{})
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("plan-preview-seal",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewSealPipe{} })
}

func TestPreviewPlan_RefusesASideEffectInsideAPredicate(t *testing.T) {
	reg, ok := sparkwing.Lookup("plan-preview-seal")
	if !ok {
		t.Fatal("pipeline did not register")
	}
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "plan-preview-seal"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	previewSideEffects.Store(0)
	preview, err := sparkwingruntime.PreviewPlan(plan, "plan-preview-seal", nil, sparkwingruntime.PreviewOptions{})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}

	// safety: the counter reads zero both when the guard refused and when the
	// preview evaluated nothing, so the recorded refusal is what proves reach.
	var detail string
	var found bool
	for _, node := range preview.Nodes {
		if node.Work == nil {
			continue
		}
		for _, item := range node.Work.Steps {
			if item.ID == "probe" {
				detail, found = item.SkipDetail, true
			}
		}
	}
	if !found {
		t.Fatal("the preview never evaluated the probe step, so it proves nothing about the seal")
	}
	if !strings.Contains(detail, "called inside Pipeline.Plan()") {
		t.Fatalf("probe skip detail = %q, want the guard's refusal", detail)
	}
	if got := previewSideEffects.Load(); got != 0 {
		t.Fatalf("the predicate body ran past the guard %d time(s)", got)
	}
}
