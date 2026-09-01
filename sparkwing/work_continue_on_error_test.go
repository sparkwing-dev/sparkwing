package sparkwing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestRunWork_DefaultFailFastCancelsSiblings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		siblingEntered := make(chan struct{})
		boom := errors.New("boom")
		w := NewWork()
		var siblingCancelled atomic.Bool
		Step(w, "fast-fail", func(ctx context.Context) error {
			select {
			case <-siblingEntered:
				return boom
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		Step(w, "slow", func(ctx context.Context) error {
			close(siblingEntered)
			<-ctx.Done()
			siblingCancelled.Store(true)
			return ctx.Err()
		})
		_, err := RunWork(t.Context(), w)
		if !errors.Is(err, boom) {
			t.Fatalf("RunWork error = %v, want fast-fail error", err)
		}
		if !siblingCancelled.Load() {
			t.Error("slow sibling did not observe cancellation; default is fail-fast")
		}
	})
}

func TestRunWork_ContinueOnErrorKeepsSiblingsAlive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		siblingEntered := make(chan struct{})
		failureReturned := make(chan struct{})
		w := NewWork()
		var siblingCompleted atomic.Bool
		Step(w, "fast-fail", func(ctx context.Context) error {
			<-siblingEntered
			close(failureReturned)
			return errors.New("boom")
		}).ContinueOnError()
		Step(w, "slow", func(ctx context.Context) error {
			close(siblingEntered)
			<-failureReturned
			if err := ctx.Err(); err != nil {
				return err
			}
			siblingCompleted.Store(true)
			return nil
		}).ContinueOnError()
		_, err := RunWork(t.Context(), w)
		if !siblingCompleted.Load() {
			t.Error("slow sibling should have completed; ContinueOnError must not cancel siblings")
		}
		if err == nil {
			t.Fatal("RunWork should still surface the failure on the run rollup")
		}
	})
}

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
