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
			waits := 0
			got, err := nodehost.FollowReconciliation(t.Context(), f, request, func(_ context.Context, gotRef store.NodeRef, after int64) error {
				waits++
				if gotRef != ref || after != 7 {
					t.Errorf("followed %v after %d", gotRef, after)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if waits != 1 || len(f.requests) != 2 || got.Kind != end.Kind || got.Node.Attempt != 5 || got.Node.Generation != end.Node.Generation {
				t.Fatalf("result=%+v waits=%d calls=%d", got, waits, len(f.requests))
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
