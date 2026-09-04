package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestDispatchState_TerminalWritesSurviveACancelledRunContext(t *testing.T) {
	tests := []struct {
		name    string
		nodeID  string
		mark    func(s *dispatchState, nodeID string)
		outcome sparkwing.Outcome
	}{
		{
			name:    "cancelled",
			nodeID:  "waiter",
			mark:    func(s *dispatchState, id string) { s.markCancelled(id, "ctx-cancelled") },
			outcome: sparkwing.Cancelled,
		},
		{
			name:    "skipped",
			nodeID:  "recovery",
			mark:    func(s *dispatchState, id string) { s.markSkipped(id, `parent "victim" did not fail`) },
			outcome: sparkwing.Skipped,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := consumerTestStore(t, t.TempDir())
			ctx := context.Background()
			runID := "run-" + test.name
			if err := st.CreateRun(ctx, store.Run{
				ID: runID, Pipeline: "deploy", Status: "running", StartedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: runID, NodeID: test.nodeID, Status: "pending"}); err != nil {
				t.Fatal(err)
			}

			dead, cancel := context.WithCancel(context.Background())
			cancel()
			s := &dispatchState{
				ctx:      dead,
				backends: Backends{State: localState{st: st}},
				runID:    runID,
				outcomes: map[string]sparkwing.Outcome{},
			}
			test.mark(s, test.nodeID)

			n, err := st.GetNode(ctx, runID, test.nodeID)
			if err != nil || n == nil {
				t.Fatalf("get node: %v", err)
			}
			if n.Outcome != string(test.outcome) {
				t.Fatalf("node outcome = %q, status = %q, want %q: the run context was already cancelled when the node reached its terminal state",
					n.Outcome, n.Status, test.outcome)
			}
		})
	}
}
