package nodehost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOwnerHostApprovalStopsBeforeComputeLayers(t *testing.T) {
	f, host, input := newOwnerFixture(t, true)
	f.snapshot.Run.State = store.RunRunning
	f.snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "gate"}, Status: store.StatusPending}}
	var layers []string
	host.Layer = func(_ context.Context, layer LayerSpec, _ AcceptedRun, _ store.NodeRow) (OwnerLayerResult, error) {
		layers = append(layers, layer.Name)
		if layer.Name == "memo" || layer.Name == "admission" {
			return OwnerLayerResult{}, errors.New("approval entered compute policy")
		}
		return OwnerLayerResult{}, nil
	}
	host.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
		if s.Nodes[0].Status == string(store.StatusPending) {
			return store.ScheduleDecision{Nodes: []store.NodeDecision{{NodeID: "gate", Kind: store.DecisionApproval}}}
		}
		return store.ScheduleDecision{}
	}
	waiting := errors.New("waiting for human decision")
	wait := host.Wait
	host.Wait = func(ctx context.Context, s store.RunSnapshot) error {
		if s.Nodes[0].Status == store.StatusApprovalPending {
			return waiting
		}
		return wait(ctx, s)
	}
	_, err := host.Run(t.Context(), input)
	if !errors.Is(err, waiting) {
		t.Fatalf("approval did not reach human wait: %v", err)
	}
	if !reflect.DeepEqual(layers, []string{"debug", "skip_if", "approval"}) {
		t.Fatalf("approval layers = %v", layers)
	}
}

func TestOwnerHostAcquisitionUsesPersistedAcceptance(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	in.Accepted = nil
	h.Acquire = func(_ context.Context, got RunInput) (*source.Entry, error) {
		if got.Accepted == nil || got.Accepted.SpecHash != string(f.snapshot.Run.Spec.Hash) {
			t.Error("source did not receive the accepted seal")
		}
		return &source.Entry{}, nil
	}
	if _, err := h.Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerHostRefusesChangedResumeBeforeAcquisition(t *testing.T) {
	_, h, in := newOwnerFixture(t, true)
	in.Accepted.PlanHash = "changed"
	h.Acquire = func(context.Context, RunInput) (*source.Entry, error) {
		t.Error("acquired changed resume")
		return &source.Entry{}, nil
	}
	if _, err := h.Run(t.Context(), in); err == nil {
		t.Fatal("accepted changed resume")
	}
}

func TestOwnerHostBlockedLayerDoesNotBlockSibling(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	for _, id := range []string{"blocked", "sibling"} {
		f.snapshot.Nodes = append(f.snapshot.Nodes, store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: id}, Status: store.StatusPending})
	}
	blocked := make(chan struct{})
	released := make(chan struct{})
	h.Decide = func(s store.ScheduleSnapshot) store.ScheduleDecision {
		d := store.ScheduleDecision{}
		for _, row := range s.Nodes {
			if row.Outcome == "" {
				d.Nodes = append(d.Nodes, store.NodeDecision{NodeID: row.ID, Kind: store.DecisionReady})
			}
		}
		if len(d.Nodes) == 0 {
			d.Outcome = "success"
		}
		return d
	}
	h.Layer = func(ctx context.Context, layer LayerSpec, _ AcceptedRun, row store.NodeRow) (OwnerLayerResult, error) {
		if layer.Name != "debug" {
			t.Fatalf("unexpected layer after terminal transition: %s", layer.Name)
		}
		if row.Ref.NodeID == "blocked" {
			close(blocked)
			select {
			case <-released:
			case <-ctx.Done():
				return OwnerLayerResult{}, context.Cause(ctx)
			}
		} else {
			<-blocked
			close(released)
		}
		return OwnerLayerResult{Transition: &store.NodeTransition{To: store.NodeSkipped, Cause: store.CauseDependency}}, nil
	}
	if _, err := h.Run(t.Context(), in); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerHostCommittedFailureRemainsSchedulable(t *testing.T) {
	f, h, _ := newOwnerFixture(t, true)
	sentinel := errors.New("attempt timed out")
	for _, status := range []store.NodeStatus{store.StatusPending, store.StatusDone} {
		f.snapshot.Nodes = []store.NodeRow{{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: status}}
		if status == store.StatusDone {
			f.snapshot.Nodes[0].Outcome = store.NodeFailed
		}
		err := h.executionError(t.Context(), "run", ownerExecution{nodeID: "job", err: sentinel})
		if status == store.StatusDone && err != nil {
			t.Fatal("committed job failure aborted its owner", err)
		}
		if status == store.StatusPending && !errors.Is(err, sentinel) {
			t.Fatal("uncommitted failure was swallowed", err)
		}
	}
}

func TestOwnerHostRemoteDispatchNeverSelfClaims(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	row := store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}
	f.snapshot.Nodes = []store.NodeRow{row}
	h.Layer = func(context.Context, LayerSpec, AcceptedRun, store.NodeRow) (OwnerLayerResult, error) {
		return OwnerLayerResult{}, nil
	}
	request, err := h.release(t.Context(), in, *in.Accepted, row, store.DecisionReady)
	if err != nil || request != nil {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	if len(f.trace) != 1 || f.trace[0] != "ready" {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostBindingCannotReplaceSelfClaim(t *testing.T) {
	f, h, in := newOwnerFixture(t, true)
	h.Executor = ownerNodeFunc(func(context.Context, NodeInput) error { t.Error("executed a changed claim"); return nil })
	h.Bind = func(_ context.Context, a AcceptedRun, r store.ReconciledNode, e *source.Entry) (NodeInput, error) {
		return NodeInput{Accepted: a, NodeID: r.Node.Ref.NodeID, Generation: r.Claim.Generation + 1, Authority: ClaimAuthority{Holder: r.Claim.Holder, CapabilityPrefix: r.Claim.CapabilityPrefix}}, nil
	}
	err := h.execute(t.Context(), *in.Accepted, &source.Entry{}, store.ReconcileRequest{Ref: store.NodeRef{RunID: "run", NodeID: "job"}, Actor: in.Owner})
	if err == nil {
		t.Fatal("accepted changed generation")
	}
	if len(f.trace) != 1 || f.trace[0] != "claim" {
		t.Fatalf("trace=%v", f.trace)
	}
}

func TestOwnerHostWaitRetainsFollowerCleanupFailure(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "job completed"}[completed], func(t *testing.T) {
			_, h, _ := newOwnerFixture(t, true)
			cleanup := errors.New("subscription did not join")
			h.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
				<-ctx.Done()
				return errors.Join(ctx.Err(), cleanup)
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			result := make(chan ownerExecution, 1)
			cause := errors.New("owner revoked")
			if completed {
				result <- ownerExecution{nodeID: "build"}
			} else {
				cancel(cause)
			}
			_, err := h.wait(ctx, store.RunSnapshot{}, result)
			if !errors.Is(err, cleanup) || !completed && !errors.Is(err, cause) {
				t.Fatalf("wait discarded cleanup or cancellation cause: %v", err)
			}
		})
	}
}

func TestOwnerHostWaitIgnoresOnlyItsRequestedCancellation(t *testing.T) {
	_, h, _ := newOwnerFixture(t, true)
	h.Wait = func(ctx context.Context, _ store.RunSnapshot) error {
		<-ctx.Done()
		return errors.Join(ctx.Err())
	}
	done := make(chan ownerExecution, 1)
	done <- ownerExecution{nodeID: "build"}
	result, err := h.wait(t.Context(), store.RunSnapshot{}, done)
	if err != nil || result == nil || result.nodeID != "build" {
		t.Fatalf("wait = %+v, %v", result, err)
	}
}
