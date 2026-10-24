package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// TransitionJob carries only the requested edge and result to the transaction.
// Actors, claim fences, events, replay and clocks are server-owned facts.
func (s *EngineController) TransitionJob(ctx context.Context, a capability.Authorized, body controllerwire.JobTransitionRequest) (controllerwire.Applied, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	change, err := nodeTransitionForStore(a, body)
	if err != nil {
		return controllerwire.Applied{}, err
	}

	applied, err := tenant.TransitionNode(ctx, change)
	return appliedResponse(applied), engineLookupError(err)
}

// Finish ends the run and executable owner in one transaction. Publication uses
// its separate store edge and never supplies an executable owner lease.
func (s *EngineController) Finish(ctx context.Context, a capability.Authorized, body controllerwire.FinishRequest) (controllerwire.Applied, error) {
	request, err := finishForStore(a, body)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	var applied runstate.Applied
	if request.Transition.Cause == runstate.RunPublish {
		applied, err = tenant.TransitionRun(ctx, runstate.RunTransition{RunID: request.Transition.RunID, Actor: request.Transition.Actor, Cause: request.Transition.Cause, Reason: request.Transition.Reason, MaskedError: request.Transition.MaskedError})
	} else {
		applied, err = tenant.FinishRunAndUnit(ctx, request)
	}
	return appliedResponse(applied), engineLookupError(err)
}

// cancel requests cooperative cancellation under the caller's tagged authority.
func (s *EngineController) cancel(ctx context.Context, a capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	actor := runstate.Actor{Authority: a.Principal.Authority, Kind: runstate.ActorOperator, CapabilityPrefix: principalCapabilityPrefix(a.Principal)}
	applied, err := tenant.TransitionRun(ctx, runstate.RunTransition{
		RunID: runspec.RunID(a.Target.RunID), To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: actor, Reason: body.Reason,
	})
	return appliedResponse(applied), engineLookupError(err)
}

// retry delegates source-seal loading to AdmitRun. The new requester is recorded
// as retrier lineage without reconstructing or changing the source's request.
func (s *EngineController) retry(ctx context.Context, a capability.Authorized, body controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
	if err := body.Validate(); err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	request, err := IntakeForStore(a, controllerwire.CreateRunRequest{
		IdempotencyKey: body.IdempotencyKey,
		Request:        runspec.Request{Version: 1, Team: a.Target.Team, Lineage: runspec.Lineage{RetryOf: a.Target.RunID}},
	})
	if err != nil {
		return controllerwire.CreateRunResponse{}, err
	}
	request.RetryMode = string(body.Mode)
	return s.admitRun(ctx, a.Target.Team, request)
}

// UnitRenewer preserves the store's atomic unit liveness and execution-start boundary.
type UnitRenewer interface {
	RenewUnit(context.Context, string, bool) (runstate.LeaseBeat, error)
}

// Renew renews the authenticated unit and records execution using the store clock.
func (s *EngineController) Renew(ctx context.Context, a capability.Authorized, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	p := a.Principal
	if p.Authority.Kind != capability.AuthorityCapability || p.Authority.ID == "" || p.Authority.ID != p.Capability.Prefix {
		return controllerwire.RenewalResponse{}, routepolicy.MissingEndpointPolicy()
	}
	units := s.Units
	if units == nil {
		return controllerwire.RenewalResponse{}, routepolicy.MissingEndpointPolicy()
	}
	if body.LocalSecrets != nil {
		if s.authority == nil {
			return controllerwire.RenewalResponse{}, routepolicy.MissingEndpointPolicy()
		}
		if err := s.authority.ResupplyHereSecrets(ctx, p.Authority, body.LocalSecrets); err != nil {
			return controllerwire.RenewalResponse{}, err
		}
	}
	beat, err := units.RenewUnit(ctx, p.Authority.ID, body.Executing)
	if err != nil {
		return controllerwire.RenewalResponse{}, engineLookupError(err)
	}
	return controllerwire.RenewalResponse{
		ExpiresAt: beat.ExpiresAt, CancelRequested: beat.CancelRequested, CancelReason: beat.CancelReason,
		Watched: beat.Watched, WatchedJobs: append([]string{}, beat.WatchedNodes...),
	}, nil
}

func appliedResponse(applied runstate.Applied) controllerwire.Applied {
	return controllerwire.Applied{
		Outcome: string(applied.Outcome), Reason: applied.Reason,
		Replayed: applied.Replayed, CancelRequested: applied.CancelRequested,
		StateSeq: applied.StateSeq, Generation: int64(applied.Generation), Attempt: int64(applied.Attempt),
	}
}

func engineMaskedError(failure *apierr.Error) (string, error) {
	if failure == nil {
		return "", nil
	}
	if failure.Code == "" || int64(len(failure.Message)) > limits.ErrorBytes {
		return "", invalidEngineBody("result error requires a code and bounded masked message")
	}
	return failure.Message, nil
}

func nodeTransitionForStore(a capability.Authorized, body controllerwire.JobTransitionRequest) (store.NodeCommand, error) {
	if a.Target.RunID == "" || a.Target.NodeID == "" {
		return nil, invalidEngineBody("transition requires a job")
	}
	ref, actor := runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, engineActor(a)
	var resultForStore *runstate.NodeResult
	if result := body.Result; result != nil {
		if int64(len(result.MaskedError)) > limits.ErrorBytes {
			return nil, invalidEngineBody("masked result error exceeds its byte limit")
		}
		resultForStore = &runstate.NodeResult{
			Outcome: runstate.NodeState(result.Outcome), Reason: result.Reason,
			MaskedError: result.MaskedError,
			ExitCode:    result.ExitCode, OutputRecord: result.Output, ManifestRecord: result.Manifest, Failure: result.Failure,
		}
		if result.Failure != nil {
			masked, err := engineMaskedError(result.Failure.Error)
			if err != nil {
				return nil, err
			}
			if result.MaskedError != "" && result.MaskedError != masked {
				return nil, invalidEngineBody("masked result error differs from its structured failure")
			}
			resultForStore.MaskedError = masked
		}
	}
	switch runstate.TransitionCause(body.Cause) {
	case runstate.CauseStart, runstate.CauseRetry, runstate.CauseReport, runstate.CausePublish, runstate.CausePublishAttempt, runstate.CauseRelease:
	default:
		return nil, invalidEngineBody("unsupported node command")
	}
	command, err := store.NodeCommandFromPayload(runstate.NodeCommandPayload{
		To: runstate.NodeState(body.To), Cause: runstate.TransitionCause(body.Cause), SourceAttempt: (*runspec.AttemptOrdinal)(body.SourceAttempt),
		Extra: runstate.NodeCommandExtra{Result: resultForStore, RetryAt: body.RetryAt, Publication: body.Publication},
	}, ref, actor)
	if err != nil {
		return nil, invalidEngineBody(err.Error())
	}
	return command, nil
}

func finishForStore(a capability.Authorized, body controllerwire.FinishRequest) (runstate.FinishRunRequest, error) {
	masked, err := engineMaskedError(body.Error)
	if err != nil {
		return runstate.FinishRunRequest{}, err
	}
	if a.Target.RunID == "" {
		return runstate.FinishRunRequest{}, invalidEngineBody("finish requires a run")
	}
	change := runstate.RunTransition{
		RunID: runspec.RunID(a.Target.RunID), Actor: engineActor(a),
		Cause: runstate.RunFinish, Reason: body.Reason, MaskedError: masked,
	}
	if a.Access.Credential == capability.CredentialPublisher {
		change.Cause = runstate.RunPublish
	}
	return runstate.FinishRunRequest{Transition: runstate.RunCompletion{RunID: change.RunID, Actor: change.Actor, Cause: change.Cause, OwnerPolicy: change.OwnerPolicy, Reason: change.Reason, MaskedError: change.MaskedError}, Unit: change.Actor.Owner}, nil
}
