package orchestrator

import (
	"context"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type cancelOnResetState struct {
	localState
	cancel context.CancelFunc
}

func (s cancelOnResetState) ResetNodeForAutoRetry(ctx context.Context, runID, nodeID string) error {
	err := s.localState.ResetNodeForAutoRetry(ctx, runID, nodeID)
	s.cancel()
	return err
}

// A run cancelled once the auto-retry reset committed, before the retry
// reached its runner, still records the node as cancelled rather than
// leaving it pending in a finished run.
func TestAutoRetryCancelledBeforeDispatchRecordsTheNode(t *testing.T) {
	paths := PathsAt(t.TempDir())
	if err := paths.EnsureRoot(); err != nil {
		t.Fatal(err)
	}
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	backends := LocalBackends(paths, st, nil)
	backends.State = cancelOnResetState{localState: localState{st: st}, cancel: cancel}
	if _, err := Run(ctx, backends, Options{RunID: "retry-cancel", Pipeline: "accounting-unrecorded-retry"}); err != nil {
		t.Logf("run: %v", err)
	}
	node, err := st.GetNode(t.Context(), "retry-cancel", "build")
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != "done" || node.Outcome != "cancelled" {
		t.Fatalf("node = %s/%q, want done/cancelled", node.Status, node.Outcome)
	}
}
