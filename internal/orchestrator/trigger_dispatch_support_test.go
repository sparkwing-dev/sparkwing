package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// TestRunAndAwaitRefusesAnObjectStoreStateBackend pins the refusal a spawning
// node gets under Mode 2. The object-store backend enqueues a trigger and has
// no claim path, so awaiting the child would wait on a run nothing starts.
func TestRunAndAwaitRefusesAnObjectStoreStateBackend(t *testing.T) {
	s := &dispatchState{backends: Backends{State: s3StateAdapter{}}}

	resolved, err := s.pipelineAwaiter().Await(context.Background(), sparkwing.AwaitRequest{
		Pipeline: "child",
		NodeID:   "out",
	})
	if err == nil {
		t.Fatalf("expected a refusal, got %+v", resolved)
	}
	if !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("error does not name the trigger refusal: %v", err)
	}
	if !errors.Is(err, storage.ErrNotSupported) {
		t.Errorf("error does not wrap storage.ErrNotSupported: %v", err)
	}
}

// TestCheckTriggerDispatchSeesThroughTheLocalMirror pins the ordinary Mode 2
// shape: a profile leaves mirror_local on, so the object-store backend arrives
// wrapped and an unwrapped type test would let the await through.
func TestCheckTriggerDispatchSeesThroughTheLocalMirror(t *testing.T) {
	wrapped := Backends{State: newMirrorStateBackend(s3StateAdapter{}, nil, nil)}
	if err := wrapped.checkTriggerDispatch(); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("a mirrored object-store backend was not refused: %v", err)
	}
}

// TestObjectStoreEnqueueRefusesTriggers covers the node that runs in its own
// process, which reaches this backend through the loopback shim rather than
// through the awaiter's guard.
func TestObjectStoreEnqueueRefusesTriggers(t *testing.T) {
	var state StateBackend = s3StateAdapter{}
	if _, err := state.EnqueueTrigger(context.Background(),
		"child", nil, "parent", "node", "", "await-pipeline", "", "", ""); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("EnqueueTrigger did not refuse: %v", err)
	}
	if _, err := enqueueTriggerWithEnv(context.Background(), state,
		"child", nil, "parent", "node", "", "await-pipeline", "", "", "", nil); !errors.Is(err, ErrTriggersUnsupported) {
		t.Errorf("EnqueueTriggerWithEnv did not refuse: %v", err)
	}
}

// TestCheckTriggerDispatchAllowsTheDispatchingBackends keeps the refusal narrow:
// the local store claims triggers itself, and a hosted controller claims them on
// its own side while leaving LocalCoordination false.
func TestCheckTriggerDispatchAllowsTheDispatchingBackends(t *testing.T) {
	hosted := client.New("http://controller.invalid", &http.Client{})
	for name, b := range map[string]Backends{
		"local":    {State: localState{}, LocalCoordination: true},
		"hosted":   {State: hosted},
		"api sock": {State: hosted, LocalCoordination: true},
	} {
		if err := b.checkTriggerDispatch(); err != nil {
			t.Errorf("%s: unexpected refusal: %v", name, err)
		}
	}
}
