package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type ownerExecution struct {
	nodeID string
	err    error
}

func (h *OwnerHost) schedule(parent context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry) (RunCompletion, error) {
	var spec runspec.Spec
	if err := json.Unmarshal(accepted.Spec, &spec); err != nil {
		return RunCompletion{}, err
	}
	skips, err := selectionSkips(accepted.Plan, spec.Resolved.Settings.Options)
	if err != nil {
		return RunCompletion{}, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	var workers sync.WaitGroup
	defer func() { cancel(context.Canceled); workers.Wait() }()
	completed := make(chan ownerExecution)
	active := map[string]context.CancelCauseFunc{}
	debugged := map[string]runspec.ClaimGeneration{}
	project, decide := h.Project, h.Decide
	if project == nil {
		project = store.RunSnapshot.Scheduling
	}
	if decide == nil {
		decide = store.Decide
	}
	launch := func(nodeID string, work func(context.Context) error) {
		workCtx, stop := context.WithCancelCause(ctx)
		active[nodeID] = stop
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer stop(nil)
			err := work(workCtx)
			var cancelled *lease.CancelError
			if errors.As(context.Cause(workCtx), &cancelled) && waitFailure(err, cancelled) == nil {
				err = h.executionError(ctx, input.RunID, ownerExecution{nodeID, err})
			}
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
		select {
		case result := <-completed:
			delete(active, result.nodeID)
			if result.err != nil {
				return RunCompletion{}, result.err
			}
			continue
		default:
		}
		snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
		if err != nil {
			return RunCompletion{}, err
		}
		if snapshot.Run.ID != input.RunID {
			return RunCompletion{}, errors.New("scheduling changed the run identity")
		}
		for _, row := range snapshot.Nodes {
			stop := active[string(row.Ref.NodeID)]
			if row.CancelRequestedAt == nil || row.Status == store.StatusDone {
				continue
			}
			if row.ClaimedBy != "" && row.OwnerCapabilityPrefix != input.Owner.CapabilityPrefix {
				continue
			}
			if row.ClaimedBy == "" {
				applied, err := h.Store.TransitionNode(ctx, store.CancelNode{Ref: row.Ref, Actor: input.Owner, Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: row.CancelReason}})
				if err != nil {
					return RunCompletion{}, err
				}
				if applied.CancelRequested {
					continue
				}
			}
			if stop != nil {
				stop(&lease.CancelError{Reason: row.CancelReason})
			}
		}
		selected := false
		for _, row := range snapshot.Nodes {
			reason, excluded := skips[string(row.Ref.NodeID)]
			if !excluded || row.Status != store.StatusPending || row.ClaimedBy != "" || row.ReadyAt != nil || snapshot.Run.CancelRequestedAt != nil {
				continue
			}
			if _, err := h.Store.TransitionNode(ctx, store.DependencyNode{Ref: row.Ref, Actor: input.Owner, Outcome: store.NodeSkipped, Result: &store.NodeResult{Outcome: store.NodeSkipped, Reason: reason}}); err != nil {
				return RunCompletion{}, err
			}
			selected = true
		}
		if selected {
			continue
		}
		for _, row := range snapshot.Nodes {
			if row.Status != store.StatusDone || active[string(row.Ref.NodeID)] != nil {
				continue
			}
			if generation, ok := debugged[string(row.Ref.NodeID)]; ok && generation == row.Generation {
				continue
			}
			debugged[string(row.Ref.NodeID)] = row.Generation
			boundary, err := debugBoundary(input, accepted, row, false)
			if err != nil {
				return RunCompletion{}, err
			}
			if boundary != nil {
				launch(string(row.Ref.NodeID), func(ctx context.Context) error { return h.debugBoundary(ctx, input, accepted, row, false) })
			}
		}
		graph, err := project(snapshot)
		if err != nil {
			return RunCompletion{}, err
		}
		for i := range graph.Nodes {
			if !graph.CancelRequested && active[graph.Nodes[i].ID] != nil && graph.Nodes[i].Status == string(store.StatusDone) {
				graph.Nodes[i].Status, graph.Nodes[i].Outcome = string(store.StatusRunning), ""
			}
		}
		decision := decide(graph)
		if decision.Outcome != "" && len(active) == 0 && len(decision.Nodes) == 0 {
			return h.finish(ctx, input, decision)
		}
		changed := false
		expanded := map[string]bool{}
		for _, action := range decision.Nodes {
			if active[action.NodeID] != nil {
				continue
			}
			row, ok := ownerNode(snapshot, action.NodeID)
			if !ok || row.Generation != runspec.ClaimGeneration(action.Generation) {
				return RunCompletion{}, errors.New("decision does not match its persisted node")
			}
			if action.Kind == store.DecisionCancelHolder && row.CancelRequestedAt != nil {
				continue
			}
			if action.Kind == store.DecisionExpand && expanded[action.NodeID] {
				continue
			}
			if action.Kind == store.DecisionReady || action.Kind == store.DecisionApproval {
				launch(action.NodeID, func(ctx context.Context) error {
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
			if active[string(row.Ref.NodeID)] != nil || row.Status == store.StatusDone {
				continue
			}
			local := h.Local != nil && h.Local(row)
			burstConcurrency := spec.Request.Request.Burst != nil && h.retainRemote && hasLocalConcurrency(row.Execution.Concurrency)
			if !local && (!h.retainRemote || !hasLocalConcurrency(row.Execution.Concurrency)) {
				continue
			}
			if row.ReadyAt != nil || row.ClaimedBy != "" {
				launch(string(row.Ref.NodeID), func(ctx context.Context) error {
					work := func(workCtx context.Context) error {
						if !local && !burstConcurrency {
							return h.followRemote(workCtx, ctx, input.Owner, row)
						}
						return h.execute(workCtx, input, accepted, entry, store.ReconcileRequest{Ref: row.Ref, Actor: input.Owner, Policy: store.SourceNone})
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
		return errors.Join(cause, waitFailure(result.err, cause))
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
		if row.Ref.NodeID == runspec.NodeID(id) && row.Ref.RunID == runspec.RunID(snapshot.Run.ID) {
			return row, true
		}
	}
	return store.NodeRow{}, false
}

func (h *OwnerHost) apply(ctx context.Context, input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow, action store.NodeDecision) error {
	var change store.NodeCommand
	switch action.Kind {
	case store.DecisionTerminal:
		outcome := store.NodeState(action.Outcome)
		change = store.DependencyNode{Ref: row.Ref, Actor: input.Owner, Outcome: outcome, Result: &store.NodeResult{Outcome: outcome, Reason: action.Reason}}
	case store.DecisionCancelHolder:
		change = store.CancelNode{Ref: row.Ref, Actor: input.Owner, Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: action.Reason}}
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
	if err := h.debugBoundary(ctx, input, accepted, row, true); err != nil {
		return err
	}
	request := store.ReconcileRequest{Ref: row.Ref, Actor: input.Owner, Policy: store.SourceNone}
	snapshot, err := h.Store.ReadSnapshot(ctx, input.RunID)
	if err != nil {
		return err
	}
	if selection := snapshot.Run.Retry; selection != nil {
		if selected, ok := selection.Results[string(row.Ref.NodeID)]; ok {
			probe := request
			probe.Policy, probe.MaterializeOnly = store.SourceManualRetry, true
			probe.Source = &store.MaterializeSource{Kind: store.SourceManualRetry, Result: &selected}
			reused, err := FollowReconciliation(ctx, h.Store, probe, h.WaitNode)
			if err != nil {
				return err
			}
			if reused.Kind != store.Dispatch {
				return nil
			}
		}
	}
	for _, layer := range LayerTable() {
		if layer.Side != OwnerSide {
			continue
		}
		result, err := h.Layer(ctx, layer, ownerEvaluation(input, accepted, entry, row), row, input.Storage)
		if err != nil {
			return err
		}
		if result.Transition != nil {
			change := store.BindNodeCommand(result.Transition, row.Ref, input.Owner)
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
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	burstConcurrency := kind != store.DecisionApproval && spec.Request.Request.Burst != nil && h.retainRemote && hasLocalConcurrency(row.Execution.Concurrency)
	parent := ctx
	work := func(ctx context.Context) error {
		var change store.NodeCommand = store.ReadyNode{Ref: row.Ref, Actor: input.Owner}
		if kind == store.DecisionApproval {
			change = store.OpenApprovalNode{Ref: row.Ref, Actor: input.Owner}
		}
		if _, err := h.Store.TransitionNode(ctx, change); err != nil {
			return err
		}
		if local || burstConcurrency {
			if err := next(ctx, request); err != nil {
				return err
			}
		} else if kind != store.DecisionApproval && h.retainRemote && hasLocalConcurrency(row.Execution.Concurrency) {
			return h.followRemote(ctx, parent, input.Owner, row)
		}
		return nil
	}
	if kind != store.DecisionApproval && h.Admit != nil {
		err := h.Admit(ctx, accepted, row, work)
		var refused *AdmissionFailure
		if errors.As(err, &refused) && waitFailure(err, refused) == nil && (refused.Outcome == store.NodeSkipped || refused.Outcome == store.NodeFailed) {
			policy := admissionPolicy(refused, int64(row.Generation))
			_, err = h.Store.TransitionNode(ctx, store.OwnerPolicyNode{Ref: row.Ref, Actor: input.Owner, Outcome: refused.Outcome, OwnerPolicy: &policy, Result: &store.NodeResult{Outcome: refused.Outcome, Reason: refused.Reason}})
		}
		return err
	}
	return work(ctx)
}

func admissionPolicy(failure *AdmissionFailure, generation int64) store.OwnerPolicyResult {
	kind := store.PolicyAdmissionFail
	if failure.Outcome == store.NodeSkipped {
		kind = store.PolicyAdmissionSkip
	} else if failure.Reason == store.FailureQueueTimeout {
		kind = store.PolicyQueueTimeout
	}
	return store.OwnerPolicyResult{Kind: kind, Generation: runspec.ClaimGeneration(generation)}
}

func (h *OwnerHost) followRemote(ctx, parent context.Context, actor store.Actor, before store.NodeRow) error {
	var stopped error
	for {
		if cause := context.Cause(ctx); cause != nil {
			var cancelled *lease.CancelError
			if stopped != nil || parent.Err() != nil || !errors.As(cause, &cancelled) {
				return errors.Join(stopped, cause)
			}
			stopped = cause
			_, err := h.Store.TransitionNode(parent, store.CancelNode{Ref: before.Ref, Actor: actor, Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "admission_cancelled"}})
			if err != nil {
				return errors.Join(stopped, err)
			}
			ctx = parent
		}
		snapshot, err := h.Store.ReadSnapshot(ctx, string(before.Ref.RunID))
		if err != nil {
			if ctx.Err() != nil && waitFailure(err, context.Cause(ctx)) == nil {
				continue
			}
			return errors.Join(stopped, err)
		}
		row, ok := ownerNode(snapshot, string(before.Ref.NodeID))
		if !ok || row.Ref != before.Ref || row.SpecHash != before.SpecHash || row.StateSeq < before.StateSeq || row.Generation < before.Generation {
			return errors.Join(stopped, errors.New("remote admission lost its persisted job identity"))
		}
		if row.Status == store.StatusDone {
			return stopped
		}
		if err := h.Wait(ctx, snapshot); err != nil && (ctx.Err() == nil || waitFailure(err, context.Cause(ctx)) != nil) {
			return errors.Join(stopped, context.Cause(ctx), err)
		}
		before = row
	}
}

func (h *OwnerHost) execute(ctx context.Context, run RunInput, accepted AcceptedRun, entry *source.Entry, request store.ReconcileRequest) error {
	if h.Executor == nil || entry == nil {
		return errors.New("self execution requires a node host and acquired entry")
	}
	spec, err := admissionSpec(accepted)
	if err != nil {
		return err
	}
	if spec.Request.Request.Burst != nil && h.claimAdmission != nil {
		return h.executeBurst(ctx, run, accepted, entry, request)
	}
	result, err := FollowReconciliation(ctx, h.Store, request, h.WaitNode)
	if err != nil {
		return err
	}
	if result.Kind != store.Execute {
		return nil
	}
	return h.executeAward(ctx, run, accepted, entry, result)
}

func (h *OwnerHost) executeAward(ctx context.Context, run RunInput, accepted AcceptedRun, entry *source.Entry, result store.ReconciledNode) error {
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
	workCtx, stop := keepNode(ctx, h.Lease, h.Store, result.Node.Ref, executorActor(input))
	defer stop()
	if err := context.Cause(workCtx); err != nil {
		return err
	}
	err = h.Executor.Execute(workCtx, input)
	return h.executionError(ctx, accepted.RunID, ownerExecution{nodeID: input.NodeID, err: errors.Join(err, context.Cause(workCtx))})
}

func ownerEvaluation(input RunInput, accepted AcceptedRun, entry *source.Entry, row store.NodeRow) EvaluationInput {
	return EvaluationInput{
		Engine: entry.Engine, Accepted: accepted, Entry: entry, GrantedRunner: input.GrantedRunner,
		NodeID: string(row.Ref.NodeID), Generation: int64(row.Generation), SpecHash: row.SpecHash,
		Storage:      input.Storage,
		RouteBinding: nodeproto.ProcessBinding{RunID: accepted.RunID, JobID: string(row.Ref.NodeID)},
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
	return h.commitExpansion(ctx, row, actor, body, failure)
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
	applied, err := h.Store.FinishRunAndUnit(ctx, store.FinishRunRequest{Unit: input.Owner.Owner, Transition: store.RunCompletion{RunID: runspec.RunID(input.RunID), Actor: input.Owner, Cause: store.RunFinish, Reason: decision.Reason}})
	if err != nil {
		return RunCompletion{}, err
	}
	return committedRunCompletion(applied)
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

func waitFailure(err error, ignored ...error) error {
	if err == nil || err == context.Canceled {
		return nil
	}
	for _, cause := range ignored {
		if cause != nil && reflect.TypeOf(cause).Comparable() && err == cause {
			return nil
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var failures []error
		for _, cause := range joined.Unwrap() {
			failures = append(failures, waitFailure(cause, ignored...))
		}
		return errors.Join(failures...)
	}
	// safety: the requested stop may wrap cancellation together with an independent subscription failure.
	if cause := errors.Unwrap(err); cause != nil && waitFailure(cause, ignored...) == nil {
		return nil
	}
	return err
}

func committedRunCompletion(applied store.Applied) (RunCompletion, error) {
	switch applied.Outcome {
	case store.RunSuccess, store.RunFailed, store.RunCancelled:
		return RunCompletion{Outcome: string(applied.Outcome), Reason: applied.Reason}, nil
	default:
		return RunCompletion{}, errors.New("completion returned no committed run verdict")
	}
}
