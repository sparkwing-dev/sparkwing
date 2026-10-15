package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type ownerExecution struct {
	nodeID string
	err    error
}

func (h *OwnerHost) schedule(parent context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry) (RunCompletion, error) {
	ctx, cancel := context.WithCancelCause(parent)
	var workers sync.WaitGroup
	defer func() { cancel(context.Canceled); workers.Wait() }()
	completed := make(chan ownerExecution)
	active := map[string]bool{}
	project, decide := h.Project, h.Decide
	if project == nil {
		project = store.RunSnapshot.Scheduling
	}
	if decide == nil {
		decide = store.Decide
	}
	launch := func(nodeID string, work func() error) {
		active[nodeID] = true
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := work()
			select {
			case completed <- ownerExecution{nodeID, err}:
			case <-ctx.Done():
			}
		}()
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return RunCompletion{}, err
		}
		snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
		if err != nil {
			return RunCompletion{}, err
		}
		if snapshot.Run.ID != input.RunID {
			return RunCompletion{}, errors.New("scheduling changed the run identity")
		}
		graph, err := project(snapshot)
		if err != nil {
			return RunCompletion{}, err
		}
		decision := decide(graph)
		if decision.Outcome != "" && len(active) == 0 && len(decision.Nodes) == 0 {
			return h.finish(ctx, input, decision)
		}
		changed := false
		expanded := map[string]bool{}
		for _, action := range decision.Nodes {
			if active[action.NodeID] {
				continue
			}
			row, ok := ownerNode(snapshot, action.NodeID)
			if !ok || row.Generation != action.Generation {
				return RunCompletion{}, errors.New("decision does not match its persisted node")
			}
			if action.Kind == store.DecisionExpand && expanded[action.NodeID] {
				continue
			}
			if action.Kind == store.DecisionReady || action.Kind == store.DecisionApproval {
				launch(action.NodeID, func() error {
					return h.release(ctx, input, accepted, entry, row, action.Kind, func(ctx context.Context, request store.ReconcileRequest) error {
						return h.execute(ctx, input, accepted, entry, request)
					})
				})
				continue
			}
			if err := h.apply(ctx, input, accepted, entry, row, action); err != nil {
				return RunCompletion{}, err
			}
			expanded[action.NodeID] = action.Kind == store.DecisionExpand
			changed = true
		}
		// safety: ready work and live holders can predate this owner; neither needs a second release event.
		for _, row := range snapshot.Nodes {
			if active[row.Ref.NodeID] || h.Local == nil || !h.Local(row) || row.Status == store.StatusDone {
				continue
			}
			if row.ReadyAt != nil || row.ClaimedBy != "" {
				launch(row.Ref.NodeID, func() error {
					work := func(ctx context.Context) error {
						return h.execute(ctx, input, accepted, entry, store.ReconcileRequest{Ref: row.Ref, Actor: input.Owner, Policy: store.SourceNone})
					}
					if h.Admit != nil {
						return h.Admit(ctx, accepted, row, work)
					}
					return work(ctx)
				})
			}
		}
		select {
		case result := <-completed:
			delete(active, result.nodeID)
			if result.err != nil {
				return RunCompletion{}, result.err
			}
			continue
		default:
		}
		if changed {
			continue
		}
		result, err := h.wait(ctx, snapshot, completed)
		if err != nil {
			return RunCompletion{}, err
		}
		if result != nil {
			delete(active, result.nodeID)
			if result.err != nil {
				return RunCompletion{}, result.err
			}
		}
	}
}

func (h *OwnerHost) executionError(ctx context.Context, runID string, result ownerExecution) error {
	if result.err == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	snapshot, err := h.Store.ReadSnapshot(ctx, runID)
	if err != nil {
		return errors.Join(result.err, err)
	}
	row, ok := ownerNode(snapshot, result.nodeID)
	if ok && row.Status == store.StatusDone && row.Outcome != "" {
		return nil
	}
	return result.err
}

func ownerNode(snapshot store.RunSnapshot, id string) (store.NodeRow, bool) {
	for _, row := range snapshot.Nodes {
		if row.Ref.NodeID == id && row.Ref.RunID == snapshot.Run.ID {
			return row, true
		}
	}
	return store.NodeRow{}, false
}

func (h *OwnerHost) apply(ctx context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow, action store.NodeDecision) error {
	change := store.NodeTransition{Ref: row.Ref, Actor: input.Owner}
	switch action.Kind {
	case store.DecisionTerminal:
		change.To, change.Cause = store.NodeState(action.Outcome), store.CauseDependency
		change.Extra.Result = &store.NodeResult{Outcome: change.To, Reason: action.Reason}
	case store.DecisionCancelHolder:
		change.To, change.Cause = store.NodeCancelled, store.CauseCancel
		change.Extra.Result = &store.NodeResult{Outcome: store.NodeCancelled, Reason: action.Reason}
	case store.DecisionExpand:
		return h.expand(ctx, input, accepted, entry, row)
	default:
		return fmt.Errorf("unknown scheduling decision %q", action.Kind)
	}
	_, err := h.Store.TransitionNode(ctx, change)
	return err
}

func (h *OwnerHost) release(ctx context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow, kind store.DecisionKind, next func(context.Context, store.ReconcileRequest) error) error {
	if h.Layer == nil {
		return errors.New("run host requires owner layer policies before dispatch")
	}
	request := store.ReconcileRequest{Ref: row.Ref, Actor: input.Owner, Policy: store.SourceNone}
	for _, layer := range LayerTable() {
		if layer.Side != OwnerSide {
			continue
		}
		result, err := h.Layer(ctx, layer, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
		if err != nil {
			return err
		}
		if result.Transition != nil {
			change := *result.Transition
			change.Ref, change.Actor = row.Ref, input.Owner
			_, err = h.Store.TransitionNode(ctx, change)
			return err
		}
		if result.Source != nil {
			request.Source, request.Policy = result.Source, result.Source.Kind
			probe := request
			probe.MaterializeOnly = true
			reused, err := FollowReconciliation(ctx, h.Store, probe, h.WaitNode)
			if err != nil {
				return err
			}
			if reused.Kind != store.Dispatch {
				return nil
			}
			request.Source, request.Policy = nil, store.SourceNone
		}
		if kind == store.DecisionApproval && layer.Name == "approval" {
			break
		}
	}
	local := kind != store.DecisionApproval && h.Local != nil && h.Local(row)
	work := func(ctx context.Context) error {
		change := store.NodeTransition{Ref: row.Ref, Actor: input.Owner, To: store.NodeReady, Cause: store.CauseRelease}
		if kind == store.DecisionApproval {
			change.To, change.Cause = store.NodeApprovalPending, store.CauseApprovalOpen
		}
		if _, err := h.Store.TransitionNode(ctx, change); err != nil {
			return err
		}
		if local {
			return next(ctx, request)
		}
		return nil
	}
	if kind != store.DecisionApproval && h.Admit != nil {
		err := h.Admit(ctx, accepted, row, work)
		if refused, ok := err.(*AdmissionFailure); ok && refused.Outcome == store.NodeSkipped {
			_, err = h.Store.TransitionNode(ctx, store.NodeTransition{
				Ref: row.Ref, Actor: input.Owner, To: store.NodeSkipped, Cause: store.CauseDependency,
				Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSkipped, Reason: refused.Reason}},
			})
		}
		return err
	}
	return work(ctx)
}

func (h *OwnerHost) execute(ctx context.Context, run RunInput, accepted AcceptedRun, entry *source.Entry, request store.ReconcileRequest) error {
	if h.Executor == nil || entry == nil {
		return errors.New("self execution requires a node host and acquired entry")
	}
	result, err := FollowReconciliation(ctx, h.Store, request, h.WaitNode)
	if err != nil {
		return err
	}
	if result.Kind != store.Execute {
		return nil
	}
	input, err := NewNodeInput(accepted, result.Node, entry.Engine, run.GrantedRunner)
	if err != nil {
		return err
	}
	if len(result.Bearer) != 0 && h.AdoptClaim == nil {
		return errors.New("self execution requires private award adoption")
	}
	if h.AdoptClaim != nil {
		if err := h.AdoptClaim(ctx, result); err != nil {
			return err
		}
	}
	input.Entry, input.Storage = entry, run.Storage
	return h.executionError(ctx, accepted.RunID, ownerExecution{nodeID: input.NodeID, err: h.Executor.Execute(ctx, input)})
}

func ownerEvaluation(input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow) EvaluationInput {
	return EvaluationInput{
		Engine: entry.Engine, Accepted: accepted, Entry: entry, GrantedRunner: input.GrantedRunner,
		NodeID: row.Ref.NodeID, Generation: row.Generation, SpecHash: row.SpecHash,
		RouteBinding: nodeproto.ProcessBinding{RunID: accepted.RunID, JobID: row.Ref.NodeID},
	}
}

func (h *OwnerHost) expand(ctx context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow) error {
	if h.Expand == nil {
		return errors.New("run host requires a generator evaluator")
	}
	body, failure, err := h.Expand(ctx, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
	if err != nil {
		return err
	}
	if (len(body) == 0) == (failure == nil) {
		return errors.New("generator must return declarations or a structured failure")
	}
	actor, err := h.expansionActor(ctx, input, row)
	if err != nil {
		return err
	}
	commit := func() error {
		_, err := h.Store.AcceptExpansion(ctx, store.ExpansionSubmission{Source: row.Ref, SourceGeneration: row.Generation, Actor: actor, Body: body, Failure: failure})
		return err
	}
	if h.Masks != nil && failure == nil {
		var proposal nodeproto.PlanDoc
		if err := json.Unmarshal(body, &proposal); err != nil {
			return err
		}
		return h.Masks.Accept(proposal, commit)
	}
	return commit()
}

func (h *OwnerHost) finish(ctx context.Context, input RunInput, decision store.ScheduleDecision) (RunCompletion, error) {
	if err := context.Cause(ctx); err != nil {
		return RunCompletion{}, err
	}
	switch store.RunState(decision.Outcome) {
	case store.RunSuccess, store.RunFailed, store.RunCancelled:
	default:
		return RunCompletion{}, errors.New("schedule returned an invalid run verdict")
	}
	_, err := h.Store.FinishRunAndUnit(ctx, store.FinishRunRequest{Unit: input.Owner.Owner, Transition: store.RunTransition{RunID: input.RunID, To: store.RunState(decision.Outcome), Actor: input.Owner, Cause: store.RunFinish, Reason: decision.Reason}})
	if err != nil {
		return RunCompletion{}, err
	}
	return RunCompletion{Outcome: decision.Outcome, Reason: decision.Reason}, nil
}

func (h *OwnerHost) wait(ctx context.Context, snapshot store.RunSnapshot, completed <-chan ownerExecution) (*ownerExecution, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- h.Wait(waitCtx, snapshot) }()
	select {
	case err := <-done:
		cancel()
		return nil, errors.Join(err, context.Cause(ctx))
	case result := <-completed:
		cancel()
		return &result, waitFailure(<-done)
	case <-ctx.Done():
		cancel()
		return nil, errors.Join(context.Cause(ctx), waitFailure(<-done))
	}
}

func waitFailure(err error) error {
	if err == nil || err == context.Canceled {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var failures []error
		for _, cause := range joined.Unwrap() {
			failures = append(failures, waitFailure(cause))
		}
		return errors.Join(failures...)
	}
	// safety: the requested stop may wrap cancellation together with an independent subscription failure.
	if cause := errors.Unwrap(err); cause != nil && waitFailure(cause) == nil {
		return nil
	}
	return err
}
