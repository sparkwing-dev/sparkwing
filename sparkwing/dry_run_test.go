package sparkwing_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDryRun_StepWithDryRunFn_DryRunCalledApplySkipped(t *testing.T) {
	var applyCount, dryCount atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "apply-thing", func(ctx context.Context) error {
		applyCount.Add(1)
		return nil
	}).DryRun(func(ctx context.Context) error {
		dryCount.Add(1)
		return nil
	})

	ctx := sparkwingruntime.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if applyCount.Load() != 0 {
		t.Errorf("apply Fn must not run under --dry-run, got %d calls", applyCount.Load())
	}
	if dryCount.Load() != 1 {
		t.Errorf("DryRunFn should run once, got %d calls", dryCount.Load())
	}
}

func TestDryRun_SafeWithoutDryRun_ApplyRunsUnchanged(t *testing.T) {
	var count atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "read-state", func(ctx context.Context) error {
		count.Add(1)
		return nil
	}).SafeWithoutDryRun()

	ctx := sparkwingruntime.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if count.Load() != 1 {
		t.Errorf("safe step should run unmodified, got %d calls", count.Load())
	}
}

func TestDryRun_StepWithoutDryRunOrSafeMarker_SoftSkipped(t *testing.T) {
	var count atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "mutates-something", func(ctx context.Context) error {
		count.Add(1)
		return nil
	})

	ctx := sparkwingruntime.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if count.Load() != 0 {
		t.Errorf("step without dry-run contract must NOT execute under --dry-run, got %d calls", count.Load())
	}
}

func TestDryRun_NotInDryRunMode_ApplyRunsAsUsual(t *testing.T) {
	var applyCount, dryCount atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "apply", func(ctx context.Context) error {
		applyCount.Add(1)
		return nil
	}).DryRun(func(ctx context.Context) error {
		dryCount.Add(1)
		return nil
	})

	if _, err := sparkwing.RunWork(context.Background(), w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if applyCount.Load() != 1 {
		t.Errorf("apply should run when not under --dry-run, got %d calls", applyCount.Load())
	}
	if dryCount.Load() != 0 {
		t.Errorf("DryRunFn must not run when not under --dry-run, got %d calls", dryCount.Load())
	}
}

func TestDryRun_DryRunFnFailure_PropagatedAsStepError(t *testing.T) {
	w := sparkwing.NewWork()
	wantErr := errors.New("plan output mismatch")
	sparkwing.Step(w, "apply", func(ctx context.Context) error {
		t.Errorf("apply Fn must not run under --dry-run when DryRunFn is defined")
		return nil
	}).DryRun(func(ctx context.Context) error {
		return wantErr
	})

	ctx := sparkwingruntime.WithDryRun(context.Background())
	_, err := sparkwing.RunWork(ctx, w)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Errorf("expected DryRunFn error to surface, got %v", err)
	}
}

type previewDryRunJob struct{ sparkwing.Base }

func (previewDryRunJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "with-dry-run", func(ctx context.Context) error { return nil }).
		DryRun(func(ctx context.Context) error { return nil })
	sparkwing.Step(w, "safe-without-dry-run", func(ctx context.Context) error { return nil }).
		SafeWithoutDryRun()
	sparkwing.Step(w, "missing-contract", func(ctx context.Context) error { return nil })
	return nil, nil
}

type previewDryRunPipe struct{ sparkwing.Base }

func (previewDryRunPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "build", previewDryRunJob{})
	return nil
}

func TestPreviewPlan_DryRunRendersThreeDecisions(t *testing.T) {
	sparkwing.Register[sparkwing.NoInputs]("dry-run-preview",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewDryRunPipe{} })
	reg, _ := sparkwing.Lookup("dry-run-preview")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "dry-run-preview"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	preview, err := sparkwingruntime.PreviewPlan(plan, "dry-run-preview", nil, sparkwingruntime.PreviewOptions{DryRun: true})
	if err != nil {
		t.Fatalf("PreviewPlan: %v", err)
	}
	if len(preview.Nodes) != 1 || preview.Nodes[0].Work == nil {
		t.Fatalf("expected one node with Work")
	}
	wantDecision := map[string]string{
		"with-dry-run":         "would_dry_run",
		"safe-without-dry-run": "would_run",
		"missing-contract":     "would_skip",
	}
	wantReason := map[string]string{
		"missing-contract": "no_dry_run_defined",
	}
	for _, s := range preview.Nodes[0].Work.Steps {
		if got := s.Decision; got != wantDecision[s.ID] {
			t.Errorf("step %q decision: got %q, want %q", s.ID, got, wantDecision[s.ID])
		}
		if want, ok := wantReason[s.ID]; ok && s.SkipReason != want {
			t.Errorf("step %q skip_reason: got %q, want %q", s.ID, s.SkipReason, want)
		}
	}
}

func TestDryRun_IsDryRunReadableFromCtx(t *testing.T) {
	var seenDryRun bool
	w := sparkwing.NewWork()
	sparkwing.Step(w, "inspect", func(ctx context.Context) error {
		seenDryRun = sparkwing.IsDryRun(ctx)
		return nil
	}).SafeWithoutDryRun()

	ctx := sparkwingruntime.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !seenDryRun {
		t.Errorf("IsDryRun should return true inside a step running under WithDryRun")
	}
}
