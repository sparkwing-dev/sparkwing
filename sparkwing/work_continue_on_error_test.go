package sparkwing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunWork_DefaultFailFastCancelsSiblings pins the prior
// behavior: a sibling that errors cancels the runCtx, so other
// in-flight siblings exit with ctx.Err. Default for steps with no
// failure-handling flags.
func TestRunWork_DefaultFailFastCancelsSiblings(t *testing.T) {
	w := NewWork()
	var siblingCompleted atomic.Bool
	Step(w, "fast-fail", func(ctx context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return errors.New("boom")
	})
	Step(w, "slow", func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
			siblingCompleted.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	_, err := RunWork(context.Background(), w)
	if err == nil {
		t.Fatal("RunWork should have errored")
	}
	if siblingCompleted.Load() {
		t.Error("slow sibling should have been cancelled; default is fail-fast")
	}
}

// TestRunWork_ContinueOnErrorKeepsSiblingsAlive verifies that a
// failure on a step marked ContinueOnError does NOT cancel siblings.
// Both errored and successful siblings get a chance to complete.
// Run-level rollup still reports the failure.
func TestRunWork_ContinueOnErrorKeepsSiblingsAlive(t *testing.T) {
	w := NewWork()
	var siblingCompleted atomic.Bool
	Step(w, "fast-fail", func(ctx context.Context) error {
		time.Sleep(20 * time.Millisecond)
		return errors.New("boom")
	}).ContinueOnError()
	Step(w, "slow", func(ctx context.Context) error {
		select {
		case <-time.After(150 * time.Millisecond):
			siblingCompleted.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}).ContinueOnError()
	_, err := RunWork(context.Background(), w)
	if !siblingCompleted.Load() {
		t.Error("slow sibling should have completed; ContinueOnError must not cancel siblings")
	}
	if err == nil {
		t.Fatal("RunWork should still surface the failure on the run rollup")
	}
}

// TestRunWork_OptionalMasksRollup pins the Optional contract: a
// failing-but-optional step keeps siblings alive (ContinueOnError
// implied) AND does not contribute to the Job's rollup outcome.
func TestRunWork_OptionalMasksRollup(t *testing.T) {
	w := NewWork()
	Step(w, "best-effort", func(ctx context.Context) error {
		return errors.New("nope")
	}).Optional()
	Step(w, "real-work", func(ctx context.Context) error {
		return nil
	})
	_, err := RunWork(context.Background(), w)
	if err != nil {
		t.Errorf("RunWork should report nil; Optional failure must not roll up: got %v", err)
	}
}

// TestRunWork_ContinueOnErrorLetsDependentsRun pins the cascade
// rule: a ContinueOnError step's dependents still dispatch (mirrors
// Plan-layer Job.ContinueOnError). Without the flag, dependents
// cascade-skip and never run.
func TestRunWork_ContinueOnErrorLetsDependentsRun(t *testing.T) {
	w := NewWork()
	var downstreamRan atomic.Bool
	upstream := Step(w, "upstream", func(ctx context.Context) error {
		return errors.New("upstream failed")
	}).ContinueOnError()
	Step(w, "downstream", func(ctx context.Context) error {
		downstreamRan.Store(true)
		return nil
	}).Needs(upstream)

	_, _ = RunWork(context.Background(), w)
	if !downstreamRan.Load() {
		t.Error("downstream should run when its upstream is ContinueOnError")
	}
}

// TestRunWork_DefaultCascadeSkipsDependents pins the inverse of the
// ContinueOnError dependent rule. Without the flag, dep-cascade
// applies and downstream never runs.
func TestRunWork_DefaultCascadeSkipsDependents(t *testing.T) {
	w := NewWork()
	var downstreamRan atomic.Bool
	upstream := Step(w, "upstream", func(ctx context.Context) error {
		return errors.New("upstream failed")
	})
	Step(w, "downstream", func(ctx context.Context) error {
		downstreamRan.Store(true)
		return nil
	}).Needs(upstream)

	_, _ = RunWork(context.Background(), w)
	if downstreamRan.Load() {
		t.Error("downstream should NOT run on plain upstream failure (cascade-skip)")
	}
}

// TestWorkStep_OptionalImpliesContinueOnError pins the convenience:
// Optional() sets both fields so the surface stays consistent with
// Job.Optional at the Plan layer.
func TestWorkStep_OptionalImpliesContinueOnError(t *testing.T) {
	w := NewWork()
	s := Step(w, "x", func(context.Context) error { return nil }).Optional()
	if !s.IsContinueOnError() {
		t.Error("Optional() should imply ContinueOnError")
	}
	if !s.IsOptional() {
		t.Error("Optional() should set IsOptional")
	}
}
