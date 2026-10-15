package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type hostReconciler struct {
	results  []store.ReconciledNode
	requests []store.ReconcileRequest
}

func (f *hostReconciler) ReconcileNode(_ context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	f.requests = append(f.requests, request)
	if len(f.results) == 0 {
		return store.ReconciledNode{}, errors.New("unexpected reconciliation")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

// CheckHostReconciliation exercises event-following and claim fences with the
// store contract faked; transaction and placement proofs remain separate cells.
func CheckHostReconciliation(t *testing.T) {
	t.Helper()
	for _, terminal := range []bool{false, true} {
		name := "requeued"
		if terminal {
			name = "terminal"
		}
		t.Run(name, func(t *testing.T) {
			ref := store.NodeRef{RunID: "run", NodeID: "job"}
			expires := time.Unix(60, 0)
			end := store.ReconciledNode{Kind: store.Execute, Node: store.NodeRow{Ref: ref, Status: store.StatusPending, Generation: 3, Attempt: 5, StateSeq: 9, ClaimedBy: "new", CapabilityPrefix: "claim", LeaseExpiresAt: &expires}, Claim: &store.ClaimAward{Holder: "new", CapabilityPrefix: "claim", Generation: 3, LeaseExpiresAt: expires}}
			if terminal {
				end = store.ReconciledNode{Kind: store.KeepTerminal, Node: store.NodeRow{Ref: ref, Status: store.StatusDone, Outcome: store.NodeSuccess, Generation: 2, Attempt: 5, StateSeq: 8}}
			}
			f := &hostReconciler{results: []store.ReconciledNode{
				{Kind: store.WaitHolder, Node: store.NodeRow{Ref: ref, Status: store.StatusRunning, Generation: 2, Attempt: 5, StateSeq: 7}}, end,
			}}
			request := store.ReconcileRequest{Ref: ref, Actor: store.Actor{Kind: store.ActorRunHost, Holder: "owner"}, Policy: store.SourceResume}
			waits, joins := 0, 0
			reader := reconciliationReader{node: end.Node}
			follower := nodehost.StateFollower{Store: reader, Subscribe: func(_ context.Context, runID string) (nodehost.StateSubscription, error) {
				waits++
				if runID != ref.RunID {
					t.Errorf("followed run %q", runID)
				}
				// safety: persistence already advanced before subscription; no notification will arrive for this commit.
				return nodehost.StateSubscription{Changed: make(chan struct{}), Close: func() error { joins++; return nil }}, nil
			}}
			got, err := nodehost.FollowReconciliation(t.Context(), f, request, follower.WaitNode)
			if err != nil {
				t.Fatal(err)
			}
			if waits != 1 || joins != 1 || len(f.requests) != 2 || got.Kind != end.Kind || got.Node.Attempt != 5 || got.Node.Generation != end.Node.Generation {
				t.Fatalf("result=%+v waits=%d joins=%d calls=%d", got, waits, joins, len(f.requests))
			}
			for _, seen := range f.requests {
				if seen != request {
					t.Fatal("reconciliation changed authority")
				}
			}
		})
	}
	t.Run("unfenced_execution", func(t *testing.T) {
		ref := store.NodeRef{RunID: "run", NodeID: "job"}
		f := &hostReconciler{results: []store.ReconciledNode{{Kind: store.Execute, Node: store.NodeRow{Ref: ref, Status: store.StatusPending}}}}
		if _, err := nodehost.FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, nil); err == nil {
			t.Fatal("accepted execution without its committed claim")
		}
	})
}

type reconciliationReader struct{ node store.NodeRow }

func (r reconciliationReader) ReadNode(_ context.Context, ref store.NodeRef) (store.NodeRow, error) {
	if r.node.Ref != ref {
		return store.NodeRow{}, errors.New("unexpected followed node")
	}
	return r.node, nil
}

func (reconciliationReader) ReadSnapshot(context.Context, string) (store.RunSnapshot, error) {
	return store.RunSnapshot{}, errors.New("node follower read a graph")
}
