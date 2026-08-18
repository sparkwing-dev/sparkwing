package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProgressTimeoutResetReplacesTheInactivityWindow(t *testing.T) {
	ctx, controller, cancel := newProgressTimeoutContext(context.Background(), time.Hour)
	defer cancel()

	controller.mu.Lock()
	previousGeneration := controller.timerGen
	controller.mu.Unlock()
	controller.reset()
	controller.finishDeadline(previousGeneration)
	select {
	case <-ctx.Done():
		t.Fatal("timeout used the original deadline after progress")
	default:
	}
}

func TestProgressTimeoutNestedPausesResumeAfterTheLastWaiter(t *testing.T) {
	ctx, controller, cancel := newProgressTimeoutContext(context.Background(), time.Hour)
	defer cancel()

	resumeFirst := controller.pause()
	resumeSecond := controller.pause()
	controller.mu.Lock()
	pausedGeneration := controller.timerGen
	controller.mu.Unlock()
	controller.finishDeadline(pausedGeneration)
	resumeFirst()
	select {
	case <-ctx.Done():
		t.Fatal("timeout fired while a nested pause remained active")
	default:
	}
	resumeSecond()
	controller.mu.Lock()
	resumedGeneration := controller.timerGen
	controller.mu.Unlock()
	controller.finishDeadline(resumedGeneration)
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("Err() = %v, want deadline exceeded", ctx.Err())
		}
	default:
		t.Fatal("timeout did not restart after the final pause")
	}
}

func TestProgressTimeoutPreservesParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), time.Second)
	defer cancelParent()
	ctx, _, cancel := newProgressTimeoutContext(parent, 2*time.Second)
	defer cancel()

	want, ok := parent.Deadline()
	if !ok {
		t.Fatal("parent has no deadline")
	}
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("Deadline() = (%v, %v), want (%v, true)", got, ok, want)
	}
}

func TestProgressTimeoutDoesNotBoundDelegatedChildWait(t *testing.T) {
	ctx, _, cancel := newProgressTimeoutContext(context.Background(), time.Hour)
	defer cancel()
	if childAwaitBounded(ctx, 0) {
		t.Fatal("a suspended progress timeout must not disable the dispatch watchdog")
	}
}

func TestNodeTimeoutDoesNotOwnParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := newNodeTimeoutContext(parent, time.Hour)
	defer cancel()
	cancelParent()
	<-ctx.Done()
	controller := nodeTimeoutControllerFromContext(ctx)
	if controller.timedOut() {
		t.Fatal("parent cancellation must not classify as node timeout expiry")
	}
}
