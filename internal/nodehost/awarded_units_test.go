package nodehost

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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
	row := store.NodeRow{Ref: controllerwire.NodeRef{RunID: input.RunID, NodeID: "source"}, Generation: 0, SpecHash: "source-hash", Status: store.StatusDone, Outcome: store.NodeMemoized, Expansion: store.ExpansionExpected}
	state.snapshot.Nodes = []store.NodeRow{row}
	s := &evalUnitStore{ownerFixture: state}
	host.Store = s
	input.Owner = store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "eval"}, CapabilityPrefix: "eval", Holder: "source-holder", Generation: 0}
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
		if evaluation.Generation != 0 || evaluation.NodeID != row.Ref.NodeID || evaluation.SpecHash != row.SpecHash || observed.Generation != 0 {
			t.Fatal("eval replaced the source identity with its lease or a work attempt")
		}
		return json.RawMessage(`{"jobs":[]}`), nil, nil
	}
	if err := host.Evaluate(t.Context(), input, row.Ref.NodeID, int64(row.Generation), fence); err != nil {
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
