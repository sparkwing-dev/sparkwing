package sparkwing_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// IMP-014: under WithDryRun(ctx), a step's DryRunFn must run in
// place of its apply Fn. Pin both with separate counters so a
// regression that calls both (or the wrong one) is obvious.
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

	ctx := sparkwing.WithDryRun(context.Background())
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

// SafeWithoutDryRun marks the apply Fn as read-only by author
// contract, so it runs unmodified under --dry-run. Pin the contract
// so a regression doesn't accidentally skip safe steps.
func TestDryRun_SafeWithoutDryRun_ApplyRunsUnchanged(t *testing.T) {
	var count atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "read-state", func(ctx context.Context) error {
		count.Add(1)
		return nil
	}).SafeWithoutDryRun()

	ctx := sparkwing.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if count.Load() != 1 {
		t.Errorf("safe step should run unmodified, got %d calls", count.Load())
	}
}

// A step with neither DryRunFn nor SafeWithoutDryRun marker is
// soft-skipped under --dry-run with reason `no_dry_run_defined`.
// Soft-skip (not panic) is the v1 behavior so existing pipelines
// keep working under --dry-run; IMP-015 will tighten this when
// blast-radius markers ship.
func TestDryRun_StepWithoutDryRunOrSafeMarker_SoftSkipped(t *testing.T) {
	var count atomic.Int64
	w := sparkwing.NewWork()
	sparkwing.Step(w, "mutates-something", func(ctx context.Context) error {
		count.Add(1)
		return nil
	})

	ctx := sparkwing.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if count.Load() != 0 {
		t.Errorf("step without dry-run contract must NOT execute under --dry-run, got %d calls", count.Load())
	}
}

// Outside dry-run mode, the apply Fn runs as normal even when a
// DryRunFn is registered -- the registration is silent unless
// WithDryRun(ctx) is in scope.
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

// A DryRunFn that fails surfaces the error through RunWork the same
// way an apply-Fn failure would. Important so the operator sees
// "your dry-run is broken" rather than a silent success.
func TestDryRun_DryRunFnFailure_PropagatedAsStepError(t *testing.T) {
	w := sparkwing.NewWork()
	wantErr := errors.New("plan output mismatch")
	sparkwing.Step(w, "apply", func(ctx context.Context) error {
		t.Errorf("apply Fn must not run under --dry-run when DryRunFn is defined")
		return nil
	}).DryRun(func(ctx context.Context) error {
		return wantErr
	})

	ctx := sparkwing.WithDryRun(context.Background())
	_, err := sparkwing.RunWork(ctx, w)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Errorf("expected DryRunFn error to surface, got %v", err)
	}
}

// IMP-014 + IMP-013 integration: PreviewPlan(opts.DryRun=true)
// renders Decision according to the dry-run lens. Three cases on
// one Work pin the contract:
//   - DryRunFn declared    -> would_dry_run
//   - SafeWithoutDryRun    -> would_run (apply Fn is read-only)
//   - neither              -> would_skip + reason no_dry_run_defined
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
	sparkwing.Register[sparkwing.NoInputs]("imp014-preview",
		func() sparkwing.Pipeline[sparkwing.NoInputs] { return previewDryRunPipe{} })
	reg, _ := sparkwing.Lookup("imp014-preview")
	plan, err := reg.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "imp014-preview"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	preview, err := sparkwing.PreviewPlan(plan, "imp014-preview", nil, sparkwing.PreviewOptions{DryRun: true})
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

// IsDryRun pivots the user's own step body if they want to branch
// inside the apply path -- some helpers (e.g. structured-log
// "would do X" for a command that has no dry-run flag of its own)
// need to read the mode directly.
func TestDryRun_IsDryRunReadableFromCtx(t *testing.T) {
	var seenDryRun bool
	w := sparkwing.NewWork()
	sparkwing.Step(w, "inspect", func(ctx context.Context) error {
		seenDryRun = sparkwing.IsDryRun(ctx)
		return nil
	}).SafeWithoutDryRun()

	ctx := sparkwing.WithDryRun(context.Background())
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if !seenDryRun {
		t.Errorf("IsDryRun should return true inside a step running under WithDryRun")
	}
}
