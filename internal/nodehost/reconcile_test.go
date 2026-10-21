package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type reconciliationFixture struct {
	results  []store.ReconciledNode
	requests []store.ReconcileRequest
	onCall   func()
}

func (f *reconciliationFixture) ReconcileNode(_ context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	f.requests = append(f.requests, request)
	if f.onCall != nil {
		f.onCall()
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

func TestFollowReconciliationPreservesCancellationDuringStoreCall(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("owner lease rejected")
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	f := &reconciliationFixture{
		onCall:  func() { cancel(cause) },
		results: []store.ReconciledNode{{Kind: store.KeepTerminal, Node: store.NodeRow{Ref: ref, Status: store.StatusDone, Outcome: store.NodeSuccess}}},
	}
	if _, err := FollowReconciliation(ctx, f, store.ReconcileRequest{Ref: ref}, nil); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause = %v", err)
	}
}

func TestFollowReconciliationWaitsAndReclaims(t *testing.T) {
	expires := time.Unix(60, 0)
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	f := &reconciliationFixture{results: []store.ReconciledNode{
		{Kind: store.WaitHolder, Node: store.NodeRow{Ref: ref, Status: store.StatusRunning, StateSeq: 4, Generation: 2, Attempt: 3}},
		{Kind: store.WaitHolder, Node: store.NodeRow{Ref: ref, Status: store.StatusRunning, StateSeq: 5, Generation: 2, Attempt: 3}},
		{Kind: store.Execute, Node: store.NodeRow{Ref: ref, Status: store.StatusPending, StateSeq: 7, Generation: 3, Attempt: 3, ClaimedBy: "new", CapabilityPrefix: "claim", LeaseExpiresAt: &expires}, Claim: &store.ClaimAward{Holder: "new", Generation: 3, CapabilityPrefix: "claim", LeaseExpiresAt: expires}},
	}}
	var followed []int64
	request := store.ReconcileRequest{Ref: ref, Actor: store.Actor{NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "owner"}}, Policy: store.SourceResume}
	result, err := FollowReconciliation(t.Context(), f, request, func(_ context.Context, got controllerwire.NodeRef, after int64) error {
		if got != ref {
			t.Fatalf("ref = %#v", got)
		}
		followed = append(followed, after)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != store.Execute || result.Node.Attempt != 3 || result.Claim.Generation != 3 {
		t.Fatalf("result = %#v", result)
	}
	if len(followed) != 2 || followed[0] != 4 || followed[1] != 5 || len(f.requests) != 3 {
		t.Fatalf("followed %v, requests %d", followed, len(f.requests))
	}
	for _, got := range f.requests {
		if got.Ref != request.Ref || got.Actor != request.Actor || got.Policy != request.Policy {
			t.Fatal("reconciliation changed request authority")
		}
	}
}

func TestFollowReconciliationKeepsTerminalWithoutWriting(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	f := &reconciliationFixture{results: []store.ReconciledNode{{Kind: store.KeepTerminal, Node: store.NodeRow{Ref: ref, Status: store.StatusDone, Outcome: store.NodeSuccess}}}}
	result, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, func(context.Context, controllerwire.NodeRef, int64) error {
		t.Fatal("terminal row followed")
		return nil
	})
	if err != nil || result.Kind != store.KeepTerminal {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestFollowReconciliationStopsOnWaitFailure(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	f := &reconciliationFixture{results: []store.ReconciledNode{{Kind: store.WaitHolder, Node: store.NodeRow{Ref: ref, StateSeq: 4}}}}
	sentinel := errors.New("event stream unavailable")
	_, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, func(context.Context, controllerwire.NodeRef, int64) error { return sentinel })
	if !errors.Is(err, sentinel) || len(f.requests) != 1 {
		t.Fatalf("error = %v; requests %d", err, len(f.requests))
	}
}

func TestFollowReconciliationRejectsUnfencedExecution(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	for _, mutation := range []string{"no claim", "no expiry", "wrong holder", "wrong generation", "finished"} {
		t.Run(mutation, func(t *testing.T) {
			expires := time.Unix(60, 0)
			result := store.ReconciledNode{
				Kind:  store.Execute,
				Node:  store.NodeRow{Ref: ref, Status: store.StatusPending, Generation: 3, ClaimedBy: "holder", CapabilityPrefix: "claim", LeaseExpiresAt: &expires},
				Claim: &store.ClaimAward{Generation: 3, Holder: "holder", CapabilityPrefix: "claim", LeaseExpiresAt: expires},
			}
			switch mutation {
			case "no claim":
				result.Claim = nil
			case "no expiry":
				result.Claim.LeaseExpiresAt = time.Time{}
			case "wrong holder":
				result.Claim.Holder = "other"
			case "wrong generation":
				result.Claim.Generation++
			case "finished":
				result.Node.Status = store.StatusDone
				result.Node.Outcome = store.NodeSuccess
			}
			f := &reconciliationFixture{results: []store.ReconciledNode{result}}
			if _, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, nil); err == nil {
				t.Fatal("execution without a matching claim accepted")
			}
		})
	}
}

func TestFollowReconciliationMaterializeMissNeverAwardsExecution(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	for _, kind := range []store.ReconcileKind{store.Dispatch, store.Execute} {
		f := &reconciliationFixture{results: []store.ReconciledNode{{Kind: kind, Node: store.NodeRow{Ref: ref, Status: store.StatusPending}}}}
		got, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref, MaterializeOnly: true}, nil)
		if kind == store.Dispatch && (err != nil || got.Kind != store.Dispatch) {
			t.Fatalf("dispatch miss: %+v, %v", got, err)
		}
		if kind == store.Execute && err == nil {
			t.Fatal("probe acquired execution")
		}
	}
	f := &reconciliationFixture{results: []store.ReconciledNode{{Kind: store.Dispatch, Node: store.NodeRow{Ref: ref, Status: store.StatusPending}}}}
	if _, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, nil); err == nil {
		t.Fatal("execution reconcile silently returned dispatch")
	}
}

func TestFollowReconciliationAcceptsReadyPlacementDispatch(t *testing.T) {
	ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
	ready := time.Unix(60, 0)
	f := &reconciliationFixture{results: []store.ReconciledNode{{Kind: store.Dispatch, Node: store.NodeRow{Ref: ref, Status: store.StatusPending, ReadyAt: &ready}}}}
	got, err := FollowReconciliation(t.Context(), f, store.ReconcileRequest{Ref: ref}, nil)
	if err != nil || got.Kind != store.Dispatch {
		t.Fatalf("placement dispatch: %s %v", got.Kind, err)
	}
}
