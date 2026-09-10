package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type refStubState struct {
	latestErr error
	output    []byte
	outputErr error

	events []recordedEvent
}

type recordedEvent struct {
	runID   string
	nodeID  string
	kind    string
	payload []byte
}

func (s *refStubState) GetLatestRun(context.Context, string, []string, time.Duration) (*store.Run, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	return &store.Run{ID: "run-1"}, nil
}

func (s *refStubState) GetNodeOutput(context.Context, string, string) ([]byte, error) {
	if s.outputErr != nil {
		return nil, s.outputErr
	}
	return s.output, nil
}

func (s *refStubState) AppendEvent(_ context.Context, runID, nodeID, kind string, payload []byte) error {
	s.events = append(s.events, recordedEvent{runID, nodeID, kind, payload})
	return nil
}

func resolveRefErr(state *refStubState) error {
	_, err := newPipelineRefResolver(state, "run-consumer", func(context.Context, string, error) {})(
		context.Background(), "build", "artifact", time.Hour)
	return err
}

func TestPipelineRefReportsAMissingPriorRunAsAbsent(t *testing.T) {
	err := resolveRefErr(&refStubState{latestErr: fmt.Errorf("latest run: %w", store.ErrNotFound)})
	if !errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a pipeline with no prior run must resolve as absent, got %v", err)
	}
}

func TestPipelineRefReportsAMissingNodeOutputAsAbsent(t *testing.T) {
	err := resolveRefErr(&refStubState{outputErr: fmt.Errorf("node output: %w", store.ErrNotFound)})
	if !errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a run that holds no such node must resolve as absent, got %v", err)
	}
}

// An unreachable store must stay unmarked, or Ref.TryGet sends a
// compare-to-last-run step down its bootstrap branch for the whole outage.
func TestPipelineRefLeavesAnUnreachableStoreUnmarked(t *testing.T) {
	unreachable := errors.New("dial tcp 127.0.0.1:4343: connect: connection refused")

	if err := resolveRefErr(&refStubState{latestErr: unreachable}); errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a run lookup that could not reach the store was reported as absent: %v", err)
	}
	if err := resolveRefErr(&refStubState{outputErr: unreachable}); errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("an output read that could not reach the store was reported as absent: %v", err)
	}
}

// A node still running inside a run recorded as successful is a broken
// assumption rather than an absence, so it stays unmarked and crashes the step.
func TestPipelineRefLeavesAnUnfinishedNodeUnmarked(t *testing.T) {
	err := resolveRefErr(&refStubState{outputErr: fmt.Errorf("node output: %w", store.ErrLockHeld)})
	if errors.Is(err, sparkwing.ErrRefAbsent) {
		t.Fatalf("a node that has not finished was reported as absent: %v", err)
	}
}

func TestPipelineRefRecordsTheRunItRead(t *testing.T) {
	state := &refStubState{output: []byte(`{"digest":"sha256:abc"}`)}
	ctx := sparkwingruntime.WithNode(context.Background(), "compare")

	resolved, err := newPipelineRefResolver(state, "run-consumer", func(_ context.Context, _ string, err error) {
		t.Errorf("audit warned: %v", err)
	})(ctx, "build", "artifact", time.Hour)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.RunID != "run-1" || string(resolved.Data) != `{"digest":"sha256:abc"}` {
		t.Fatalf("resolved %+v", resolved)
	}
	if len(state.events) != 1 {
		t.Fatalf("want one audit event, got %v", state.events)
	}
	ev := state.events[0]
	if ev.runID != "run-consumer" || ev.nodeID != "compare" || ev.kind != "pipeline_ref_resolved" {
		t.Errorf("event addressed to %s/%s as %q", ev.runID, ev.nodeID, ev.kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["source_run_id"] != "run-1" || payload["pipeline"] != "build" || payload["node_id"] != "artifact" {
		t.Errorf("payload %v", payload)
	}
}

// A resolution outside a dispatched node has nothing to attribute the read to.
func TestPipelineRefRecordsNothingWithoutAConsumingNode(t *testing.T) {
	state := &refStubState{output: []byte(`{}`)}
	if err := resolveRefErr(state); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(state.events) != 0 {
		t.Fatalf("want no audit event, got %v", state.events)
	}
}
