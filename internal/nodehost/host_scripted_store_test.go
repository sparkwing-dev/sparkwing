package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type scriptedHostStore struct {
	readSnapshot     func(context.Context, string) (store.RunSnapshot, error)
	readNode         func(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
	transitionNode   func(context.Context, store.NodeCommand) (store.Applied, error)
	transitionRun    func(context.Context, store.RunTransition) (store.Applied, error)
	recordStep       func(context.Context, store.StepReport) error
	renewOwner       func(context.Context, store.Actor, bool) (store.LeaseBeat, error)
	renewNodeClaim   func(context.Context, controllerwire.NodeRef, store.Actor) (store.LeaseBeat, error)
	acceptPlan       func(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error)
	finishRunAndUnit func(context.Context, store.FinishRunRequest) (store.Applied, error)
	acceptExpansion  func(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error)
}

func (s *scriptedHostStore) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	if s.readSnapshot == nil {
		return store.RunSnapshot{}, errors.New("unexpected run snapshot")
	}
	return s.readSnapshot(ctx, run)
}

func (s *scriptedHostStore) ReadNode(ctx context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
	if s.readNode == nil {
		return store.NodeRow{}, errors.New("unexpected node read")
	}
	return s.readNode(ctx, ref)
}

func (s *scriptedHostStore) TransitionNode(ctx context.Context, change store.NodeCommand) (store.Applied, error) {
	if s.transitionNode == nil {
		return store.Applied{}, errors.New("unexpected node transition")
	}
	return s.transitionNode(ctx, change)
}

func (s *scriptedHostStore) TransitionRun(ctx context.Context, change store.RunTransition) (store.Applied, error) {
	if s.transitionRun == nil {
		return store.Applied{}, errors.New("unexpected run transition")
	}
	return s.transitionRun(ctx, change)
}

func (s *scriptedHostStore) RecordStep(ctx context.Context, step store.StepReport) error {
	if s.recordStep == nil {
		return errors.New("unexpected step report")
	}
	return s.recordStep(ctx, step)
}

func (s *scriptedHostStore) RenewOwner(ctx context.Context, actor store.Actor, computing bool) (store.LeaseBeat, error) {
	if s.renewOwner == nil {
		return store.LeaseBeat{}, errors.New("unexpected owner renewal")
	}
	return s.renewOwner(ctx, actor, computing)
}

func (s *scriptedHostStore) RenewNodeClaim(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	if s.renewNodeClaim == nil {
		return store.LeaseBeat{}, errors.New("unexpected claim renewal")
	}
	return s.renewNodeClaim(ctx, ref, actor)
}

func (s *scriptedHostStore) AcceptPlan(ctx context.Context, plan store.PlanAcceptance) (store.AcceptedPlan, error) {
	if s.acceptPlan == nil {
		return store.AcceptedPlan{}, errors.New("unexpected plan acceptance")
	}
	return s.acceptPlan(ctx, plan)
}

func (s *scriptedHostStore) FinishRunAndUnit(ctx context.Context, finish store.FinishRunRequest) (store.Applied, error) {
	if s.finishRunAndUnit == nil {
		return store.Applied{}, errors.New("unexpected run completion")
	}
	return s.finishRunAndUnit(ctx, finish)
}

func (s *scriptedHostStore) AcceptExpansion(ctx context.Context, in store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	if s.acceptExpansion == nil {
		return store.AcceptedExpansion{}, errors.New("unexpected expansion")
	}
	return s.acceptExpansion(ctx, in)
}

func (s *scriptedHostStore) ReconcileNode(context.Context, store.ReconcileRequest) (store.ReconciledNode, error) {
	return store.ReconciledNode{}, errors.New("unexpected reconciliation")
}

type ownerSealFixture struct {
	*scriptedHostStore
	row        store.RunRow
	trace      []string
	refused    error
	renewError error
	beat       store.LeaseBeat
	finished   store.FinishRunRequest
}
type claimedHostFixture struct {
	*scriptedHostStore
	snapshot store.RunSnapshot
	beats    atomic.Int64
	mode     string
	reports  []capturedNodeCommand
}
type executionHostFixtureState struct {
	*claimedHostFixture
	attempt int64
	reject  store.TransitionCause
	steps   []store.StepReport
	nodes   map[string]store.NodeRow
}
type expansionHostFixture struct {
	*ownerSealFixture
	node       store.NodeRow
	nodes      []store.NodeRow
	expansions []store.AcceptedExpansionRow
	submission store.ExpansionSubmission
	count      int
	reject     error
}

func newOwnerSealStore(row store.RunRow) *ownerSealFixture {
	s := &ownerSealFixture{scriptedHostStore: &scriptedHostStore{}, row: row}
	s.renewOwner = func(context.Context, store.Actor, bool) (store.LeaseBeat, error) {
		return s.beat, s.renewError
	}
	s.readSnapshot = func(context.Context, string) (store.RunSnapshot, error) {
		return store.RunSnapshot{Run: s.row}, nil
	}
	s.acceptPlan = func(_ context.Context, input store.PlanAcceptance) (store.AcceptedPlan, error) {
		s.trace = append(s.trace, "accept")
		if s.refused != nil {
			return store.AcceptedPlan{}, s.refused
		}
		if !reflect.DeepEqual(input.Seal.Request, s.row.Request) {
			return store.AcceptedPlan{}, errors.New("host replaced intake")
		}
		s.row.Plan, s.row.PlanHash = input.Seal.Plan, ownerPlanHash
		s.row.Spec = &runspec.Spec{Declarations: input.Seal.Declarations, Request: input.Seal.Request, Hash: "spec", PlanHash: ownerPlanHash, Resolved: input.Seal.Resolved}
		return store.AcceptedPlan{Spec: *s.row.Spec, Plan: s.row.Plan, PlanHash: s.row.PlanHash}, nil
	}
	s.transitionRun = func(_ context.Context, input store.RunTransition) (store.Applied, error) {
		if s.row.Spec == nil {
			return store.Applied{}, errors.New("started before acceptance")
		}
		s.trace = append(s.trace, "start")
		s.row.State = input.To
		return store.Applied{}, nil
	}
	s.finishRunAndUnit = func(ctx context.Context, input store.FinishRunRequest) (store.Applied, error) {
		s.trace = append(s.trace, "finish")
		s.finished = input
		if s.row.Spec == nil {
			s.row.State = store.RunFailed
			return store.Applied{Outcome: s.row.State, Reason: input.Transition.Reason}, nil
		}
		snapshot, err := s.ReadSnapshot(ctx, s.row.ID)
		if err != nil {
			return store.Applied{}, err
		}
		graph, err := snapshot.Scheduling()
		if err != nil {
			return store.Applied{}, err
		}
		decision := store.Decide(graph)
		s.row.State = store.RunState(decision.Outcome)
		return store.Applied{Outcome: s.row.State, Reason: decision.Reason}, nil
	}
	return s
}

func newClaimedHostStore(snapshot store.RunSnapshot, mode string) *claimedHostFixture {
	s := &claimedHostFixture{scriptedHostStore: &scriptedHostStore{}, snapshot: snapshot, mode: mode}
	s.readSnapshot = func(context.Context, string) (store.RunSnapshot, error) {
		return s.snapshot, nil
	}
	s.readNode = func(context.Context, controllerwire.NodeRef) (store.NodeRow, error) {
		return s.snapshot.Nodes[0], nil
	}
	s.transitionNode = func(_ context.Context, transition store.NodeCommand) (store.Applied, error) {
		s.reports = append(s.reports, captureNodeCommand(transition))
		return store.Applied{}, nil
	}
	s.renewNodeClaim = func(_ context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
		row := s.snapshot.Nodes[0]
		if ref != row.Ref || actor.Kind != store.ActorHolder || actor.Holder != row.ClaimedBy || actor.CapabilityPrefix != row.CapabilityPrefix || actor.Generation != row.Generation || actor.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.CapabilityPrefix}) {
			return store.LeaseBeat{}, errors.New("renewal changed claim authority")
		}
		beats := s.beats.Add(1)
		switch {
		case s.mode == "rejected" || s.mode == "lost_during_work" && beats > 1:
			return store.LeaseBeat{}, &apierr.Error{Code: apierr.LockHeld}
		case s.mode == "cancelled":
			return store.LeaseBeat{CancelRequested: true, CancelReason: "operator requested stop"}, nil
		case s.mode == "silent":
			return store.LeaseBeat{}, errors.New("unavailable")
		default:
			return store.LeaseBeat{}, nil
		}
	}
	return s
}

func newExecutionHostStore(snapshot store.RunSnapshot) *executionHostFixtureState {
	s := &executionHostFixtureState{claimedHostFixture: newClaimedHostStore(snapshot, "")}
	s.recordStep = func(context.Context, store.StepReport) error { return nil }
	s.transitionNode = func(_ context.Context, command store.NodeCommand) (store.Applied, error) {
		change := captureNodeCommand(command)
		row := s.snapshot.Nodes[0]
		if change.Ref != row.Ref || change.Actor.Kind != store.ActorHolder || change.Actor.Holder != row.ClaimedBy || change.Actor.CapabilityPrefix != row.CapabilityPrefix || change.Actor.Generation != row.Generation || change.Actor.Authority != (capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: row.CapabilityPrefix}) {
			return store.Applied{}, errors.New("transition changed claim authority")
		}
		s.reports = append(s.reports, change)
		if change.Cause == s.reject {
			return store.Applied{}, errors.New("result transaction refused")
		}
		if change.Cause == store.CauseRetry || change.Cause == store.CauseStart {
			s.attempt++
		}
		return store.Applied{Generation: row.Generation, Attempt: runspec.AttemptOrdinal(s.attempt)}, nil
	}
	return s
}

func newExpansionHostStore(row store.RunRow, node store.NodeRow) *expansionHostFixture {
	s := &expansionHostFixture{ownerSealFixture: newOwnerSealStore(row), node: node}
	s.readSnapshot = func(context.Context, string) (store.RunSnapshot, error) {
		return store.RunSnapshot{Run: s.row, Nodes: append([]store.NodeRow{s.node}, s.nodes...), Expansions: s.expansions}, nil
	}
	s.readNode = func(_ context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
		if ref != s.node.Ref {
			return store.NodeRow{}, errors.New("unexpected source read")
		}
		return s.node, nil
	}
	s.acceptExpansion = func(_ context.Context, in store.ExpansionSubmission) (store.AcceptedExpansion, error) {
		s.count++
		if s.reject != nil {
			return store.AcceptedExpansion{}, s.reject
		}
		s.submission = in
		s.node.Expansion = store.ExpansionAccepted
		if in.Failure != nil {
			s.node.Expansion = store.ExpansionFailed
		} else {
			var plan nodeproto.PlanDoc
			if err := json.Unmarshal(in.Body, &plan); err != nil {
				return store.AcceptedExpansion{}, err
			}
			hash, err := expansionHash(plan)
			if err != nil {
				return store.AcceptedExpansion{}, err
			}
			s.expansions = []store.AcceptedExpansionRow{{Source: in.Source, SourceGeneration: int64(in.SourceGeneration), Body: in.Body, BodyHash: string(hash)}}
			for _, job := range plan.Jobs {
				spec, err := json.Marshal(job)
				if err != nil {
					return store.AcceptedExpansion{}, err
				}
				hash, err := expansionHash(job)
				if err != nil {
					return store.AcceptedExpansion{}, err
				}
				s.nodes = append(s.nodes, store.NodeRow{Ref: controllerwire.NodeRef{RunID: in.Source.RunID, NodeID: runspec.NodeID(job.ID)}, Spec: spec, SpecHash: string(hash), Status: store.StatusDone, Outcome: store.NodeSuccess})
			}
		}
		return store.AcceptedExpansion{Source: in.Source, SourceGeneration: in.SourceGeneration, State: s.node.Expansion}, nil
	}
	s.transitionNode = func(_ context.Context, command store.NodeCommand) (store.Applied, error) {
		in := captureNodeCommand(command)
		for i := range s.nodes {
			if s.nodes[i].Ref == in.Ref && in.Extra.Result != nil {
				s.nodes[i].Status, s.nodes[i].Outcome = store.StatusDone, in.To
				return store.Applied{}, nil
			}
		}
		return store.Applied{}, errors.New("unexpected expansion fixture transition")
	}
	return s
}
