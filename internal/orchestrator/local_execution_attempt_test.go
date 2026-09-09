package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type localAttemptFlakyPipe struct{ sparkwing.Base }

func (localAttemptFlakyPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	attempts := 0
	sparkwing.Job(plan, "flaky", func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("first attempt fails")
		}
		return nil
	}).Retry(1, sparkwing.RetryBackoff(time.Millisecond))
	return nil
}

func init() {
	register("local-attempt-flaky", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &localAttemptFlakyPipe{} })
}

func expectedLocalExecutor(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "localhost"
	}
	return host
}

func TestRunLocal_RecordsALocalExecutionAttemptPerNode(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-ok"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	attempts, err := st.ListNodeExecutionAttempts(context.Background(), res.RunID, "orch-ok")
	if err != nil {
		t.Fatalf("ListNodeExecutionAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1; a locally executed node must be attributed", len(attempts))
	}
	got := attempts[0]
	if got.Attempt != 1 {
		t.Fatalf("attempt ordinal = %d, want 1", got.Attempt)
	}
	if got.ExecutorKind != store.ExecutorKindLocal || got.ExecutorLocation != "local" {
		t.Fatalf("attribution = kind %q location %q, want local/local", got.ExecutorKind, got.ExecutorLocation)
	}
	if want := expectedLocalExecutor(t); got.ExecutorName != want || got.ExecutorID != want {
		t.Fatalf("executor = name %q id %q, want the host %q", got.ExecutorName, got.ExecutorID, want)
	}
	if got.Outcome != string(sparkwing.Success) || got.FinishedAt == nil {
		t.Fatalf("outcome = %q finished = %v, want a closed success", got.Outcome, got.FinishedAt)
	}
}

func TestRunLocal_SequencesTheAttemptsOfARetriedNode(t *testing.T) {
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "local-attempt-flaky"})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v)", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	attempts, err := st.ListNodeExecutionAttempts(context.Background(), res.RunID, "flaky")
	if err != nil {
		t.Fatalf("ListNodeExecutionAttempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (the failure and the retry)", len(attempts))
	}
	if attempts[0].Attempt != 1 || attempts[1].Attempt != 2 {
		t.Fatalf("ordinals = %d, %d, want 1 then 2", attempts[0].Attempt, attempts[1].Attempt)
	}
	if attempts[0].Outcome != string(sparkwing.Failed) || attempts[1].Outcome != string(sparkwing.Success) {
		t.Fatalf("outcomes = %q, %q, want failed then success",
			attempts[0].Outcome, attempts[1].Outcome)
	}
	for _, attempt := range attempts {
		if attempt.ExecutorLocation != "local" || attempt.ExecutorKind != store.ExecutorKindLocal {
			t.Fatalf("attempt %d attribution = kind %q location %q, want local/local",
				attempt.Attempt, attempt.ExecutorKind, attempt.ExecutorLocation)
		}
	}
}
