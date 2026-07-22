package orchestrator

import (
	"context"
	"testing"
)

type recordingExecutionLease struct{ releases int }

func (l *recordingExecutionLease) Release() error {
	l.releases++
	return nil
}

func TestExecutionLeaseController_YieldAndResumeExchangeSegments(t *testing.T) {
	first := &recordingExecutionLease{}
	second := &recordingExecutionLease{}
	acquires := 0
	controller := newExecutionLeaseController(first, func(context.Context) (executionLease, error) {
		acquires++
		return second, nil
	})

	if err := controller.yield(); err != nil {
		t.Fatalf("yield: %v", err)
	}
	if first.releases != 1 {
		t.Fatalf("first segment releases = %d, want 1", first.releases)
	}
	if err := controller.resume(context.Background()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if acquires != 1 {
		t.Fatalf("segment reacquires = %d, want 1", acquires)
	}
	if err := controller.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if second.releases != 1 {
		t.Fatalf("second segment releases = %d, want 1", second.releases)
	}
}
