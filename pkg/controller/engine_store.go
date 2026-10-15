package controller

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func engineBadRequest(message string) error {
	return &apierr.Error{Code: apierr.BadRequest, Message: message}
}

func engineTeam(authorized capability.Authorized) (store.Team, error) {
	team := authorized.Principal.Capability.Team
	ref := authorized.Principal.Authority
	if team == "" || authorized.Target.Team != "" && authorized.Target.Team != team || ref.ID == "" || ref.Kind != capability.AuthorityCapability && ref.Kind != capability.AuthoritySession {
		return "", &apierr.Error{Code: apierr.Forbidden, Message: "authenticated team authority is required"}
	}
	return store.Team(team), nil
}

func (s *EngineController) tenant(ctx context.Context, authorized capability.Authorized) (*store.Tenant, error) {
	team, err := engineTeam(authorized)
	if err != nil {
		return nil, err
	}
	return s.Store.ForTeam(ctx, team)
}

func engineActor(authorized capability.Authorized) (store.Actor, error) {
	if _, err := engineTeam(authorized); err != nil {
		return store.Actor{}, err
	}
	p := authorized.Principal
	actor := store.Actor{Kind: store.ActorOperator, Authority: p.Authority}
	if p.Authority.Kind == capability.AuthorityCapability {
		if p.Capability.Prefix != p.Authority.ID {
			return store.Actor{}, engineBadRequest("capability authority does not match its reference")
		}
		actor.CapabilityPrefix = p.Capability.Prefix
	}
	binding := authorized.Binding
	if binding.RunID != "" && binding.RunID != authorized.Target.RunID {
		return store.Actor{}, engineBadRequest("binding names another run")
	}
	if binding.Expansion != nil {
		lease := binding.Expansion
		actor.Owner = store.OwnerLeaseRef{RunID: binding.RunID, Holder: lease.Holder, Generation: lease.Generation}
		switch lease.Kind {
		case capability.UnitTrigger:
			actor.Kind, actor.Owner.Kind = store.ActorRunHost, store.OwnerTrigger
		case capability.UnitEval:
			actor.Kind, actor.Owner.Kind = store.ActorHolder, store.OwnerEval
			actor.Owner.SourceNodeID, actor.Owner.SourceGeneration = binding.NodeID, binding.Generation
		default:
			return store.Actor{}, engineBadRequest("invalid expansion owner")
		}
		return actor, nil
	}
	switch p.Capability.Unit {
	case capability.UnitTrigger, capability.UnitPlan:
		actor.Kind = store.ActorRunHost
		actor.Owner = store.OwnerLeaseRef{Kind: store.OwnerTrigger, RunID: binding.RunID, Holder: binding.Holder, Generation: binding.Generation}
	case capability.UnitNode:
		if binding.NodeID != authorized.Target.NodeID {
			return store.Actor{}, engineBadRequest("binding names another job")
		}
		actor.Kind, actor.Holder, actor.Generation = store.ActorHolder, binding.Holder, binding.Generation
	}
	return actor, nil
}

func planForStore(authorized capability.Authorized, body controllerwire.PlanSubmission, run store.RunRow) (store.PlanAcceptance, error) {
	if authorized.Target.RunID == "" || run.ID != authorized.Target.RunID {
		return store.PlanAcceptance{}, engineBadRequest("plan requires a run")
	}
	actor, err := engineActor(authorized)
	if err != nil {
		return store.PlanAcceptance{}, err
	}
	return store.PlanAcceptance{RunID: authorized.Target.RunID, Actor: actor, Seal: runspec.SealInput{
		Request: run.Request, Declarations: body.Declarations, Resolved: body.Resolved, Plan: body.Plan,
	}}, nil
}

func nodeTransitionForStore(authorized capability.Authorized, body controllerwire.JobTransitionRequest) (store.NodeTransition, error) {
	actor, err := engineActor(authorized)
	if err != nil {
		return store.NodeTransition{}, err
	}
	if authorized.Target.RunID == "" || authorized.Target.NodeID == "" {
		return store.NodeTransition{}, engineBadRequest("transition requires a job")
	}
	change := store.NodeTransition{Ref: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, Actor: actor, To: store.NodeState(body.To), Cause: store.TransitionCause(body.Cause), SourceAttempt: body.SourceAttempt, Extra: store.TransitionExtra{RetryAt: body.RetryAt}}
	if body.Result != nil {
		result := body.Result
		change.Extra.Result = &store.NodeResult{Outcome: store.NodeState(result.Outcome), Reason: result.Reason, ExitCode: result.ExitCode, OutputRecord: result.Output, ManifestRecord: result.Manifest, Failure: result.Failure}
		if result.Failure != nil && result.Failure.Error != nil {
			change.Extra.Result.MaskedError = result.Failure.Error.Message
		}
	}
	return change, nil
}

func finishForStore(authorized capability.Authorized, body controllerwire.FinishRequest) (store.FinishRunRequest, error) {
	actor, err := engineActor(authorized)
	if err != nil {
		return store.FinishRunRequest{}, err
	}
	if authorized.Target.RunID == "" {
		return store.FinishRunRequest{}, engineBadRequest("finish requires a run")
	}
	change := store.RunTransition{RunID: authorized.Target.RunID, Actor: actor, Cause: store.RunFinish, To: store.RunState(body.Outcome), Reason: body.Reason}
	if actor.Kind == store.ActorOperator {
		change.Cause = store.RunPublish
	}
	if body.Error != nil {
		change.MaskedError = body.Error.Message
	}
	return store.FinishRunRequest{Transition: change, Unit: actor.Owner}, nil
}

func engineApplied(applied store.Applied) controllerwire.Applied {
	return controllerwire.Applied{Replayed: applied.Replayed, CancelRequested: applied.CancelRequested, StateSeq: applied.StateSeq, Generation: applied.Generation, Attempt: applied.Attempt}
}

func reconcileSourceForStore(source controllerwire.ReuseSource) store.MaterializeSource {
	result := store.MaterializeSource{Kind: store.SourcePolicy(source.Policy), Source: source.Attempt, Record: source.Record}
	if source.Result != nil {
		result.Result = &store.MaterializedResultSource{Ref: store.NodeRef{RunID: source.Result.RunID, NodeID: source.Result.JobID}, StateSeq: source.Result.StateSeq}
	}
	return result
}

func engineReuseSource(source store.MaterializeSource) controllerwire.ReuseSource {
	result := controllerwire.ReuseSource{Policy: string(source.Kind), Attempt: source.Source, Record: source.Record}
	if source.Result != nil {
		result.Result = &controllerwire.MaterializedResultSource{RunID: source.Result.Ref.RunID, JobID: source.Result.Ref.NodeID, StateSeq: source.Result.StateSeq}
	}
	return result
}

func engineAdmission(result store.AdmissionResult) controllerwire.CreateRunResponse {
	return controllerwire.CreateRunResponse{RunID: result.RunID, Number: result.Number, Request: result.Request, HereBearer: string(result.HereBearer), Replayed: result.Replayed}
}

func enginePlan(result store.AcceptedPlan) controllerwire.PlanAccepted {
	return controllerwire.PlanAccepted{Spec: result.Spec, Plan: result.Plan, PlanHash: runspec.Hash(result.PlanHash), Replayed: result.Replayed}
}

func engineJob(row store.NodeRow) controllerwire.JobDetail {
	return controllerwire.JobDetail{ID: row.Ref.NodeID, Status: string(row.Status), Outcome: string(row.Outcome), StatusDetail: row.StatusDetail, Generation: row.Generation, Attempt: row.Attempt, StateSeq: row.StateSeq, Expansion: string(row.Expansion), StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, CancelRequestedAt: row.CancelRequestedAt}
}

func engineExecutionJob(row store.NodeRow) controllerwire.ExecutionJob {
	return controllerwire.ExecutionJob{JobDetail: engineJob(row), Spec: row.Spec, SpecHash: row.SpecHash, Execution: row.Execution, ClaimedBy: row.ClaimedBy, CapabilityPrefix: row.CapabilityPrefix, ReadyAt: row.ReadyAt, LeaseExpiresAt: row.LeaseExpiresAt, ApplicationRetries: row.ApplicationRetries, Disruptions: row.Disruptions, ExpansionDigest: row.ExpansionDigest}
}

func validateEngineFailure(failure *apierr.Error, decode func([]byte) (*apierr.Error, error)) error {
	if failure == nil {
		return nil
	}
	raw, err := json.Marshal(failure)
	if err != nil {
		return engineBadRequest("invalid structured failure")
	}
	_, err = decode(raw)
	return err
}

func expansionForStore(authorized capability.Authorized, body controllerwire.ExpansionSubmission, decode func([]byte) (*apierr.Error, error)) (store.ExpansionSubmission, error) {
	binding := authorized.Binding
	if authorized.Target.RunID == "" || authorized.Target.NodeID == "" || binding.Kind != capability.BindingNode || binding.RunID != authorized.Target.RunID || binding.NodeID != authorized.Target.NodeID || body.SourceGeneration <= 0 || body.SourceGeneration != binding.Generation || binding.Expansion == nil {
		return store.ExpansionSubmission{}, engineBadRequest("expansion requires the authorized source generation")
	}
	if (len(body.Body) == 0) == (body.Failure == nil) {
		return store.ExpansionSubmission{}, engineBadRequest("expansion requires exactly one body or failure")
	}
	actor, err := engineActor(authorized)
	if err != nil {
		return store.ExpansionSubmission{}, err
	}
	if err := validateEngineFailure(body.Failure, decode); err != nil {
		return store.ExpansionSubmission{}, err
	}
	request := store.ExpansionSubmission{Source: store.NodeRef{RunID: authorized.Target.RunID, NodeID: authorized.Target.NodeID}, SourceGeneration: body.SourceGeneration, Actor: actor, Body: body.Body}
	if body.Failure != nil {
		request.Failure = &store.ExpansionFailure{Code: body.Failure.Code, MaskedError: body.Failure.Message}
	}
	return request, nil
}

func engineExpansion(result store.AcceptedExpansion) (controllerwire.ExpansionAccepted, error) {
	response := controllerwire.ExpansionAccepted{SourceGeneration: result.SourceGeneration, State: string(result.State), BodyHash: result.BodyHash, Replayed: result.Replayed, Jobs: []controllerwire.ExpandedJob{}}
	for _, row := range result.Nodes {
		var job nodeproto.JobSpec
		if err := json.Unmarshal(row.Spec, &job); err != nil {
			return controllerwire.ExpansionAccepted{}, err
		}
		response.Jobs = append(response.Jobs, controllerwire.ExpandedJob{Job: job, SpecHash: row.SpecHash, Execution: row.Execution, Current: engineExecutionJob(row)})
	}
	return response, nil
}

func engineReconciledClaim(c capability.Capability, result store.ReconciledNode, run store.RunRow) *controllerwire.ClaimedUnit {
	claim := &controllerwire.ClaimedUnit{Unit: c.Unit, Purpose: c.Purpose, Delivery: c.Delivery, UnitID: c.UnitID, RunID: result.Node.Ref.RunID, JobID: result.Node.Ref.NodeID, Binding: c.Binding, Capability: string(result.Bearer), Request: run.Request, Execution: &result.Node.Execution, LeaseExpiresAt: result.Claim.LeaseExpiresAt, OwnerDeadline: c.Binding.Deadline, Runner: nodeproto.RunnerIdentity{Name: result.Claim.Holder}}
	if c.Billing != nil {
		claim.Runner.Type = c.Billing.ExecutorClass
	}
	if run.Spec != nil {
		accepted := enginePlan(store.AcceptedPlan{Spec: *run.Spec, Plan: run.Plan, PlanHash: run.PlanHash})
		claim.Accepted = &accepted
		claim.Engine = &run.Spec.Resolved.Engine
	}
	return claim
}
