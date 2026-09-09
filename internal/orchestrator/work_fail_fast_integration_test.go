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
	failFastSlowStarted          chan struct{}
	failFastRejected             chan struct{}
	failFastSlowCancelled        chan error
	failFastCancellationObserved chan struct{}
	failFastCleanupRan           chan struct{}
)

type failFastWork struct{ sparkwing.Base }

func (failFastWork) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	slow := sparkwing.Step(work, "slow", func(ctx context.Context) error {
		close(failFastSlowStarted)
		<-ctx.Done()
		select {
		case <-failFastRejected:
		default:
			return errors.New("slow step cancelled before rejection")
		}
		failFastSlowCancelled <- ctx.Err()
		close(failFastCancellationObserved)
		return ctx.Err()
	})
	reject := sparkwing.Step(work, "reject", func(context.Context) error {
		<-failFastSlowStarted
		close(failFastRejected)
		return errors.New("rejected")
	})
	sparkwing.Step(work, "pending", func(context.Context) error {
		return errors.New("pending step ran")
	}).Needs(slow)
	cleanup := sparkwing.Step(work, "cleanup", func(ctx context.Context) error {
		select {
		case <-failFastCancellationObserved:
		default:
			return errors.New("cleanup ran before the slow step observed cancellation")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
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
	failFastRejected = make(chan struct{})
	failFastSlowCancelled = make(chan error, 1)
	failFastCancellationObserved = make(chan struct{})
	failFastCleanupRan = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paths := newPaths(t)
	result, err := orchestrator.RunLocal(ctx, paths,
		orchestrator.Options{Pipeline: "orch-work-fail-fast"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if ctx.Err() != nil {
		t.Fatalf("run exhausted the test deadline: %v", ctx.Err())
	}
	select {
	case cancellation := <-failFastSlowCancelled:
		if !errors.Is(cancellation, context.Canceled) {
			t.Fatalf("slow step error = %v, want cancellation", cancellation)
		}
	default:
		t.Fatal("slow step did not observe fail-fast cancellation")
	}
	select {
	case <-failFastCleanupRan:
	default:
		t.Fatal("cleanup did not run")
	}

	state, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() {
		if err := state.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	steps, err := state.ListNodeSteps(context.Background(), result.RunID)
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
