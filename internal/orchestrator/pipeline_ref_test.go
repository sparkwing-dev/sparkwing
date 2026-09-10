package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type refStubState struct {
	StateBackend

	latestErr error
	output    []byte
	outputErr error
}

func (s refStubState) GetLatestRun(context.Context, string, []string, time.Duration) (*store.Run, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	return &store.Run{ID: "run-1"}, nil
}

func (s refStubState) GetNodeOutput(context.Context, string, string) ([]byte, error) {
	if s.outputErr != nil {
		return nil, s.outputErr
	}
	return s.output, nil
}

func (s refStubState) AppendEvent(context.Context, string, string, string, []byte) error { return nil }

func resolveRef(t *testing.T, state pipelineRefState) error {
	t.Helper()
	resolver, ok := newPipelineRefResolver(state, "run-consumer", func(context.Context, string, error) {}).(sparkwing.PipelineResolverFunc)
	if !ok {
		t.Fatal("newPipelineRefResolver no longer returns a callable resolver")
	}
	_, err := resolver(context.Background(), "build", "artifact", time.Hour)
	return err
}

func TestPipelineRefReportsAMissingPriorRunAsAbsent(t *testing.T) {
	err := resolveRef(t, refStubState{latestErr: fmt.Errorf("latest run: %w", store.ErrNotFound)})
	if !errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a pipeline with no prior run must resolve as absent, got %v", err)
	}
}

func TestPipelineRefReportsAMissingNodeOutputAsAbsent(t *testing.T) {
	err := resolveRef(t, refStubState{outputErr: fmt.Errorf("node output: %w", store.ErrNotFound)})
	if !errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a run that stored no output for the node must resolve as absent, got %v", err)
	}
}

// An unreachable store must stay unmarked, or Ref.TryGet sends a
// compare-to-last-run step down its bootstrap branch for the whole outage.
func TestPipelineRefLeavesAnUnreachableStoreUnmarked(t *testing.T) {
	unreachable := errors.New("dial tcp 127.0.0.1:4343: connect: connection refused")

	if err := resolveRef(t, refStubState{latestErr: unreachable}); errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a run lookup that could not reach the store was reported as absent: %v", err)
	}
	if err := resolveRef(t, refStubState{outputErr: unreachable}); errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("an output read that could not reach the store was reported as absent: %v", err)
	}
}
