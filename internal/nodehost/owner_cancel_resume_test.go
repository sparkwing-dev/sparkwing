package nodehost

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type cancelledReconciliation struct {
	OwnerStore
	cancel func()
}

func (s cancelledReconciliation) ReconcileNode(context.Context, store.ReconcileRequest) (runstate.ReconciledNode, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return runstate.ReconciledNode{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "credential is not live"}
}

func TestOwnerDrainsCancelledHoldersWithoutReconciliation(t *testing.T) {
	for _, mode := range []string{"pending", "running", "in-flight"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state, host, input := newOwnerFixture(t, true)
				now := time.Now()
				state.snapshot.Run.State = runstate.RunRunning
				state.snapshot.Run.CancelRequestedAt = &now
				state.snapshot.Nodes = []runstate.NodeRow{{
					Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusPending,
					Spec:      marshalEventFixture(t, nodeproto.JobSpec{ID: "job"}),
					ClaimedBy: "holder", ReadyAt: &now, CancelRequestedAt: &now,
				}}
				if mode == "running" {
					state.snapshot.Nodes[0].Status = runstate.StatusRunning
				}
				stateStore := cancelledReconciliation{OwnerStore: state}
				if mode == "in-flight" {
					state.snapshot.Run.CancelRequestedAt = nil
					state.snapshot.Nodes[0].CancelRequestedAt = nil
					stateStore.cancel = func() {
						state.mu.Lock()
						state.snapshot.Run.CancelRequestedAt = &now
						state.snapshot.Nodes[0].CancelRequestedAt = &now
						state.snapshot.Nodes[0].StateSeq++
						state.mu.Unlock()
					}
				}
				host.Store = stateStore
				host.Local = func(runstate.NodeRow) bool { return true }
				host.Executor = ownerNodeFunc(func(context.Context, NodeInput) error {
					t.Error("cancelled holder started another attempt")
					return nil
				})
				host.Project = func(snapshot store.RunSnapshot) (store.ScheduleSnapshot, error) {
					row := snapshot.Nodes[0]
					return store.ScheduleSnapshot{CancelRequested: snapshot.Run.CancelRequestedAt != nil, Nodes: []store.ScheduleNode{{ID: "job", Status: string(row.Status), Outcome: string(row.Outcome), Claimed: row.ClaimedBy != "", Ready: true}}}, nil
				}
				host.Decide = store.Decide
				done := make(chan error, 1)
				go func() { _, err := host.Run(t.Context(), input); done <- err }()
				synctest.Wait()
				state.mu.Lock()
				state.snapshot.Nodes[0].Status, state.snapshot.Nodes[0].Outcome = runstate.StatusDone, runstate.NodeCancelled
				state.snapshot.Nodes[0].StateSeq++
				state.mu.Unlock()
				started := time.Now()
				state.changed <- struct{}{}
				if err := <-done; err != nil || state.finished.Transition.RunID != "run" {
					t.Fatalf("cancelled holders left the owner unfinished: %v", err)
				}
				if elapsed := time.Since(started); elapsed != 0 {
					t.Fatalf("completion waited %s after the holder settled", elapsed)
				}
			})
		})
	}
}
