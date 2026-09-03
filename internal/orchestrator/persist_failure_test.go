package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type finishFailingState struct {
	*fakeState
	err error
}

func (f *finishFailingState) FinishRun(_ context.Context, _, _, _ string) error { return f.err }

func TestRun_TerminalStateThatNeverPersistedFailsTheRun(t *testing.T) {
	register("persist-fail-pipe", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &okPipe{} })

	clean := newFakeBackends()
	res, err := orchestrator.Run(context.Background(),
		orchestrator.Backends{State: clean.state, Logs: clean.logs, Concurrency: clean.concurrency},
		orchestrator.Options{Pipeline: "persist-fail-pipe"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("baseline status = %q (%v), want success", res.Status, res.Error)
	}

	fakes := newFakeBackends()
	outboxErr := errors.New("s3state: runs/r/state.ndjson is queued in the local outbox, not in the object store")
	state := &finishFailingState{fakeState: fakes.state, err: outboxErr}
	res, err = orchestrator.Run(context.Background(),
		orchestrator.Backends{State: state, Logs: fakes.logs, Concurrency: fakes.concurrency},
		orchestrator.Options{Pipeline: "persist-fail-pipe"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed: a run whose terminal state the store never took must not exit as a success", res.Status)
	}
	if res.Error == nil || !strings.HasPrefix(res.Error.Error(), "persist run state:") {
		t.Fatalf("result error = %v, want it to lead with \"persist run state:\"", res.Error)
	}
	if !errors.Is(res.Error, outboxErr) {
		t.Errorf("result error = %v, want it to wrap the backend's error", res.Error)
	}
}
