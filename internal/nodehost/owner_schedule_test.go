package nodehost

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestOwnerHostApprovalStopsBeforeComputeLayers(t *testing.T) {
	f, host, input := newOwnerFixture(t, true)
	f.snapshot.Run.State = store.RunRunning
	f.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "gate"}, Status: store.StatusPending}}
	var layers []string
	host.Layer = func(_ context.Context, layer LayerSpec, _ EvaluationInput, _ store.NodeRow, binding StorageBinding) (OwnerLayerResult, error) {
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
		f.snapshot.Nodes = append(f.snapshot.Nodes, store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: id}, Status: store.StatusPending})
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
	h.Layer = func(ctx context.Context, layer LayerSpec, _ EvaluationInput, row store.NodeRow, binding StorageBinding) (OwnerLayerResult, error) {
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
		f.snapshot.Nodes = []store.NodeRow{{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: status}}
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
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}
	f.snapshot.Nodes = []store.NodeRow{row}
	h.Layer = func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
		return OwnerLayerResult{}, nil
	}
	request, err := releasedRequest(&h, t.Context(), in, *in.Accepted, &source.Entry{}, row, store.DecisionReady)
	if err != nil || request != nil {
		t.Fatalf("request=%+v error=%v", request, err)
	}
	if len(f.trace) != 1 || f.trace[0] != "ready" {
		t.Fatalf("trace=%v", f.trace)
	}
}

func FuzzOwnerHostSelfClaimPreservesExecutionIdentity(f *testing.F) {
	f.Add("granted", "team=one", uint8(0))
	f.Add("granted", "team=two", uint8(1))
	f.Add("granted", "team=three", uint8(2))
	f.Add("granted", "team=four", uint8(4))
	f.Fuzz(func(t *testing.T, name, label string, flags uint8) {
		input, state, _, _ := executorFixture(t)
		runner := nodeproto.RunnerIdentity{Name: name, Type: "agent", Labels: []string{label}}
		expires := time.Now().Add(time.Hour)
		state.row.LeaseExpiresAt = &expires
		claim := &store.ClaimAward{Holder: state.row.ClaimedBy, CapabilityPrefix: state.row.CapabilityPrefix, Generation: state.row.Generation, LeaseExpiresAt: expires}
		if flags&1 != 0 {
			claim.Generation++
		}
		probe := &ownerReconcileProbe{OwnerStore: &ownerFixture{}, result: store.ReconciledNode{Kind: store.Execute, Node: state.row, Claim: claim, Bearer: []byte("private-award")}}
		adopted, executed := false, false
		h := OwnerHost{Store: probe, Lease: lease.Keeper{Interval: time.Minute, Lease: time.Hour}, AdoptClaim: func(_ context.Context, award store.ReconciledNode) error {
			adopted = true
			if string(award.Bearer) != "private-award" {
				t.Fatal("private award lost before adoption")
			}
			if flags&4 != 0 {
				return errors.New("award adoption refused")
			}
			award.Claim.Generation++
			return nil
		}, Executor: ownerNodeFunc(func(_ context.Context, got NodeInput) error {
			executed = true
			if !reflect.DeepEqual(got.GrantedRunner, runner) || !reflect.DeepEqual(got.Engine, input.Engine) || got.Generation != state.row.Generation || got.SpecHash != state.row.SpecHash || got.Authority != input.Authority || got.Entry != input.Entry {
				t.Fatal("owner replaced persisted execution identity")
			}
			return nil
		})}
		if flags&2 != 0 {
			h.AdoptClaim = nil
		}
		err := h.execute(t.Context(), RunInput{GrantedRunner: runner}, input.Accepted, input.Entry, store.ReconcileRequest{Ref: state.row.Ref})
		valid := flags&1 == 0 && name != "" && flags&2 == 0
		if (err == nil) != (valid && flags&4 == 0) || adopted != valid || executed != (valid && flags&4 == 0) {
			t.Fatalf("flags=%d valid=%v adopted=%v executed=%v error=%v", flags, valid, adopted, executed, err)
		}
	})
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

type ownerReconcileProbe struct {
	OwnerStore
	request store.ReconcileRequest
	result  store.ReconciledNode
}

func (s *ownerReconcileProbe) ReconcileNode(_ context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	s.request = request
	return s.result, nil
}

func TestOwnerHostRemoteReuseProbesBeforeDispatch(t *testing.T) {
	for _, kind := range []store.ReconcileKind{store.Dispatch, store.Materialize, store.KeepTerminal} {
		t.Run(string(kind), func(t *testing.T) {
			f, h, input := newOwnerFixture(t, true)
			row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending}
			returned := row
			if kind != store.Dispatch {
				returned.Status, returned.Outcome = store.StatusDone, store.NodeMemoized
			}
			probe := &ownerReconcileProbe{OwnerStore: f, result: store.ReconciledNode{Kind: kind, Node: returned}}
			h.Store = probe
			h.Layer = func(_ context.Context, layer LayerSpec, _ EvaluationInput, _ store.NodeRow, binding StorageBinding) (OwnerLayerResult, error) {
				if layer.Name == "admission" && kind != store.Dispatch {
					t.Fatal("materialized hit took compute admission")
				}
				if layer.Name == "memo" {
					return OwnerLayerResult{Source: &store.MaterializeSource{Kind: store.SourceMemo}}, nil
				}
				return OwnerLayerResult{}, nil
			}
			request, err := releasedRequest(&h, t.Context(), input, *input.Accepted, &source.Entry{}, row, store.DecisionReady)
			if err != nil || request != nil || !probe.request.MaterializeOnly || probe.request.Policy != store.SourceMemo {
				t.Fatalf("probe=%+v returned=%+v error=%v", probe.request, request, err)
			}
			want := []string(nil)
			if kind == store.Dispatch {
				want = []string{"ready"}
			}
			if !reflect.DeepEqual(f.trace, want) {
				t.Fatalf("release after %s: %v", kind, f.trace)
			}
		})
	}
}

func TestOwnerHostRequestsHolderCancellationThroughTransition(t *testing.T) {
	f, h, input := newOwnerFixture(t, true)
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Generation: 4, Status: store.StatusRunning, ClaimedBy: "remote"}
	if err := h.apply(t.Context(), input, *input.Accepted, &source.Entry{}, row, store.NodeDecision{Kind: store.DecisionCancelHolder, NodeID: "job", Generation: 4}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.trace, []string{"cancelled"}) {
		t.Fatal(f.trace)
	}
}

func TestOwnerHostPassesOwnerStorageToLayerPolicies(t *testing.T) {
	_, host, input := newOwnerFixture(t, true)
	input.Storage = StorageBinding{Bytes: executorStorageBytes{}, Records: executorStorageRecords{}}
	input.GrantedRunner = nodeproto.RunnerIdentity{Name: "granted", Type: "agent", Labels: []string{"native"}}
	entry := &source.Entry{Engine: engine.Resolved{Identity: engine.Identity{Version: "selected"}}}
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}, Status: store.StatusPending, Generation: 7, SpecHash: "job-seal"}
	policies := 0
	host.Layer = func(_ context.Context, _ LayerSpec, evaluation EvaluationInput, persisted store.NodeRow, binding StorageBinding) (OwnerLayerResult, error) {
		if !reflect.DeepEqual(binding, input.Storage) {
			t.Fatal("owner policy lost its bound storage clients")
		}
		if evaluation.Entry != entry || !reflect.DeepEqual(evaluation.Engine, entry.Engine) || !reflect.DeepEqual(evaluation.GrantedRunner, input.GrantedRunner) || !reflect.DeepEqual(evaluation.Accepted, *input.Accepted) || persisted.Ref != row.Ref || evaluation.NodeID != row.Ref.NodeID || evaluation.Generation != row.Generation || evaluation.SpecHash != row.SpecHash || evaluation.RouteBinding.RunID != input.RunID || evaluation.RouteBinding.JobID != row.Ref.NodeID {
			t.Fatal("owner policy lost acquired code or persisted evaluation identity")
		}
		policies++
		return OwnerLayerResult{}, nil
	}
	if _, err := releasedRequest(&host, t.Context(), input, *input.Accepted, entry, row, store.DecisionReady); err != nil {
		t.Fatal(err)
	}
	if policies != 5 {
		t.Fatalf("owner policies=%d", policies)
	}
}

func releasedRequest(h *OwnerHost, ctx context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow, kind store.DecisionKind) (*store.ReconcileRequest, error) {
	var result *store.ReconcileRequest
	err := h.release(ctx, input, accepted, entry, row, kind, func(_ context.Context, request store.ReconcileRequest) error {
		result = &request
		return nil
	})
	return result, err
}

func TestOwnerHostAdmissionSkipDoesNotHideIndependentFailure(t *testing.T) {
	for _, joined := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip", true: "independent_error"}[joined], func(t *testing.T) {
			fixture, host, input := newOwnerFixture(t, true)
			row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.RunID, NodeID: "job"}, Status: store.StatusPending}
			fixture.snapshot.Nodes = []store.NodeRow{row}
			host.Layer = func(context.Context, LayerSpec, EvaluationInput, store.NodeRow, StorageBinding) (OwnerLayerResult, error) {
				return OwnerLayerResult{}, nil
			}
			independent := errors.New("admission cleanup refused")
			host.Admit = func(context.Context, AcceptedRun, store.NodeRow, func(context.Context) error) error {
				failure := &AdmissionFailure{Outcome: store.NodeSkipped, Err: errors.New("full group")}
				if joined {
					return errors.Join(failure, independent)
				}
				return failure
			}
			request, err := releasedRequest(&host, t.Context(), input, *input.Accepted, &source.Entry{}, row, store.DecisionReady)
			if request != nil {
				t.Fatal("refused admission reached execution")
			}
			if joined {
				if !errors.Is(err, independent) || len(fixture.trace) != 0 {
					t.Fatalf("lost independent failure: trace=%v error=%v", fixture.trace, err)
				}
			} else if err != nil || !reflect.DeepEqual(fixture.trace, []string{"skipped"}) {
				t.Fatalf("skip did not commit before readiness: trace=%v error=%v", fixture.trace, err)
			}
		})
	}
}
