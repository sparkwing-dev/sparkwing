package orchestrator_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var (
	failFastSlowStarted chan struct{}
	failFastCleanupRan  chan struct{}
)

type failFastWork struct{ sparkwing.Base }

func (failFastWork) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	slow := sparkwing.Step(w, "slow", func(ctx context.Context) error {
		close(failFastSlowStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	reject := sparkwing.Step(w, "reject", func(context.Context) error {
		<-failFastSlowStarted
		return errors.New("rejected")
	})
	sparkwing.Step(w, "pending", func(context.Context) error {
		return errors.New("pending step ran")
	}).Needs(slow)
	cleanup := sparkwing.Step(w, "cleanup", func(context.Context) error {
		close(failFastCleanupRan)
		return nil
	}).Needs(reject, slow).Finally()
	return cleanup, nil
}

type failFastPipe struct{ sparkwing.Base }

func (failFastPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "gate", failFastWork{})
	return nil
}

func init() {
	register("orch-work-fail-fast", func() sparkwing.Pipeline[sparkwing.NoInputs] { return failFastPipe{} })
}

func TestRun_WorkFailFastPersistsCancelledStepsAndRunsCleanup(t *testing.T) {
	failFastSlowStarted = make(chan struct{})
	failFastCleanupRan = make(chan struct{})
	p := newPaths(t)
	started := time.Now()
	res, err := orchestrator.RunLocal(context.Background(), p,
		orchestrator.Options{Pipeline: "orch-work-fail-fast"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("fail-fast cancellation took %s", elapsed)
	}
	select {
	case <-failFastCleanupRan:
	default:
		t.Fatal("cleanup did not run")
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	steps, err := st.ListNodeSteps(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	statuses := make(map[string]string, len(steps))
	for _, step := range steps {
		statuses[step.StepID] = step.Status
	}
	if statuses["reject"] != store.StepFailed {
		t.Fatalf("reject status = %q, want failed", statuses["reject"])
	}
	if statuses["slow"] != store.StepCancelled {
		t.Fatalf("slow status = %q, want cancelled", statuses["slow"])
	}
	if statuses["pending"] != store.StepCancelled {
		t.Fatalf("pending status = %q, want cancelled", statuses["pending"])
	}
	if statuses["cleanup"] != store.StepPassed {
		t.Fatalf("cleanup status = %q, want passed", statuses["cleanup"])
	}
}
