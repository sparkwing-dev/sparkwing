package nodehost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestPlanUnitAcceptsWithoutSchedulingOrFinishingRun(t *testing.T) {
	state, host, input := newOwnerFixture(t, false)
	host.Decide = func(store.ScheduleSnapshot) store.ScheduleDecision {
		t.Fatal("plan unit became the run owner")
		return store.ScheduleDecision{}
	}
	accepted, err := host.Plan(t.Context(), input)
	if err != nil || accepted.RunID != input.RunID || state.accepted.RunID != input.RunID || state.finished.Transition.RunID != "" {
		t.Fatalf("accepted=%+v finished=%+v error=%v", accepted, state.finished, err)
	}
	if !reflect.DeepEqual(state.trace, []string{"renew", "acquire", "prepare", "plan", "accept"}) {
		t.Fatalf("plan unit changed run lifecycle: %v", state.trace)
	}
}

type evalUnitStore struct {
	*ownerFixture
	submitted store.ExpansionSubmission
}

func (s *evalUnitStore) AcceptExpansion(_ context.Context, submission store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	s.submitted = submission
	return store.AcceptedExpansion{Source: submission.Source, SourceGeneration: submission.SourceGeneration, State: store.ExpansionAccepted}, nil
}

func TestEvalUnitUsesIndependentLeaseAndSourceGeneration(t *testing.T) {
	state, host, input := newOwnerFixture(t, true)
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: "source"}, Generation: 0, Spec: json.RawMessage(`{"id":"source"}`), SpecHash: "source-hash", Status: store.StatusDone, Outcome: store.NodeMemoized, Expansion: store.ExpansionExpected}
	state.snapshot.Nodes = []store.NodeRow{row}
	s := &evalUnitStore{ownerFixture: state}
	host.Store = s
	input.Owner = store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "eval"}, CapabilityPrefix: "eval", NodeFence: runspec.NodeFence{RunID: "run", NodeID: "job", Holder: "source-holder", Generation: 0}}
	fence := capability.LeaseRef{Kind: capability.UnitEval, ID: "evaluation", Holder: "eval-holder", Generation: 7}
	var spec struct {
		Resolved struct {
			Engine engine.Identity `json:"engine"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal(input.Accepted.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	host.Acquire = func(ctx context.Context, _ RunInput) (*source.Entry, error) {
		state.record("acquire")
		return &source.Entry{Engine: engine.Resolved{Identity: spec.Resolved.Engine}}, context.Cause(ctx)
	}
	host.Expand = func(_ context.Context, evaluation EvaluationInput, observed store.NodeRow, _ StorageBinding) (json.RawMessage, *store.ExpansionFailure, error) {
		if evaluation.Generation != 0 || runspec.NodeID(evaluation.NodeID) != row.Ref.NodeID || evaluation.SpecHash != row.SpecHash || observed.Generation != 0 {
			t.Fatal("eval replaced the source identity with its lease or a work attempt")
		}
		return json.RawMessage(`{"jobs":[]}`), nil, nil
	}
	if err := host.Evaluate(t.Context(), input, string(row.Ref.NodeID), int64(row.Generation), fence); err != nil {
		t.Fatal(err)
	}
	if s.submitted.Actor != input.Owner || s.submitted.Source != row.Ref || s.submitted.SourceGeneration != 0 || string(s.submitted.Body) != `{"jobs":[]}` || state.finished.Transition.RunID != "" {
		t.Fatalf("submission=%+v finish=%+v", s.submitted, state.finished)
	}
}

func TestPlanUnitLosesLeaseBeforeAcquiring(t *testing.T) {
	state, host, input := newOwnerFixture(t, false)
	state.renewErr = &apierr.Error{Code: apierr.Forbidden, Message: "lease lost"}
	_, err := host.Plan(t.Context(), input)
	if err == nil || !reflect.DeepEqual(state.trace, []string{"renew"}) {
		t.Fatalf("trace=%v error=%v", state.trace, err)
	}
}

func TestEvalUnitApprovalPredicateHasNoJobCapacityOrCompute(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "skip"}[skip], func(t *testing.T) {
			state, host, input := newOwnerFixture(t, true)
			job := nodeproto.JobSpec{ID: "approval", Approval: &nodeproto.ApprovalSpec{Message: "ship?"}, Closures: nodeproto.ClosureSpec{SkipIf: []string{"guard"}}}
			spec, _ := json.Marshal(job)
			row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: runspec.RunID(input.RunID), NodeID: "approval"}, Spec: spec, SpecHash: "approval-hash", Status: store.StatusPending}
			state.snapshot.Nodes = []store.NodeRow{row}
			input.Owner = store.Actor{Kind: store.ActorRunHost, CapabilityPrefix: "eval", Owner: store.OwnerLeaseRef{Kind: store.OwnerEval, EvalFence: runspec.EvalFence{RunID: runspec.RunID(input.RunID), SourceNodeID: "approval", SourceGeneration: 0, Holder: "evaluator", Generation: 7}}}
			fence := capability.LeaseRef{Kind: capability.UnitEval, ID: "evaluation", Holder: "evaluator", Generation: 7}
			host.executionAdmission = ComposeStartGate(&capacityProbe{admit: func(context.Context) (store.AcquireSlotResponse, error) {
				t.Fatal("approval acquired job capacity")
				return store.AcquireSlotResponse{}, nil
			}}, &LocalAdmission{Daemon: admissionAcquireFunc(func(context.Context, wingwire.AdmissionRequest, func(wingwire.Queued)) (Lease, error) {
				t.Fatal("approval acquired compute")
				return nil, nil
			})}, true)
			host.SkipPolicy = func(_ context.Context, in EvaluationInput, observed store.NodeRow, _ StorageBinding) (OwnerLayerResult, error) {
				if in.NodeID != "approval" || in.Generation != 0 || observed.Ref != row.Ref {
					t.Fatal("approval changed source identity")
				}
				if !skip {
					return OwnerLayerResult{}, nil
				}
				return OwnerLayerResult{Transition: store.OwnerPolicyNode{Outcome: store.NodeSkipped, OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyApprovalBypass, Generation: 0}, Result: &store.NodeResult{Outcome: store.NodeSkipped, Reason: "skip_if"}}}, nil
			}
			if err := host.Evaluate(t.Context(), input, "approval", 0, fence); err != nil {
				t.Fatal(err)
			}
			state.mu.Lock()
			defer state.mu.Unlock()
			after := state.snapshot.Nodes[0]
			if skip && after.Outcome != store.NodeSkipped || !skip && after.Status != store.StatusApprovalPending {
				t.Fatalf("approval result=%+v", after)
			}
		})
	}
}
