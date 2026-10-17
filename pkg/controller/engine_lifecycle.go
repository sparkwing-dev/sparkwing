package controller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	var applied store.Applied
	if request.Transition.Cause == store.RunPublish {
		applied, err = tenant.TransitionRun(ctx, request.Transition)
	} else {
		applied, err = tenant.FinishRunAndUnit(ctx, request)
	}
	return appliedResponse(applied), engineLookupError(err)
}

// Cancel requests cooperative cancellation under the caller's tagged authority.
func (s *EngineController) Cancel(ctx context.Context, a capability.Authorized, body controllerwire.CancelRequest) (controllerwire.Applied, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return controllerwire.Applied{}, err
	}
	actor := store.Actor{Authority: a.Principal.Authority, Kind: store.ActorOperator, CapabilityPrefix: principalCapabilityPrefix(a.Principal)}
	applied, err := tenant.TransitionRun(ctx, store.RunTransition{
		RunID: a.Target.RunID, To: store.RunCancelled, Cause: store.RunCancel, Actor: actor, Reason: body.Reason,
	})
	return appliedResponse(applied), engineLookupError(err)
}

// Retry delegates source-seal loading to AdmitRun. The new requester is recorded
// as retrier lineage without reconstructing or changing the source's request.
func (s *EngineController) Retry(ctx context.Context, a capability.Authorized, body controllerwire.RetryRequest) (controllerwire.CreateRunResponse, error) {
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
	RenewUnit(context.Context, string, bool) (store.LeaseBeat, error)
}

// Renew renews the authenticated unit and records execution using the store clock.
func (s *EngineController) Renew(ctx context.Context, a capability.Authorized, body controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	p := a.Principal
	if p.Authority.Kind != capability.AuthorityCapability || p.Authority.ID == "" || p.Authority.ID != p.Capability.Prefix {
		return controllerwire.RenewalResponse{}, missingEndpointPolicy()
	}
	units := s.Units
	if units == nil && s.Store != nil {
		units = s.Store
	}
	if units == nil {
		return controllerwire.RenewalResponse{}, missingEndpointPolicy()
	}
	if body.LocalSecrets != nil {
		if s.Store == nil {
			return controllerwire.RenewalResponse{}, missingEndpointPolicy()
		}
		if err := s.Store.ResupplyHereSecrets(ctx, p.Authority, body.LocalSecrets); err != nil {
			return controllerwire.RenewalResponse{}, err
		}
	}
	if body.Available != nil {
		if s.Store == nil {
			return controllerwire.RenewalResponse{}, missingEndpointPolicy()
		}
		if err := s.Store.RecordHereAvailability(ctx, p.Authority, *body.Available); err != nil {
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

func appliedResponse(applied store.Applied) controllerwire.Applied {
	return controllerwire.Applied{
		Replayed: applied.Replayed, CancelRequested: applied.CancelRequested,
		StateSeq: applied.StateSeq, Generation: applied.Generation, Attempt: applied.Attempt,
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

func nodeTransitionForStore(a capability.Authorized, body controllerwire.JobTransitionRequest) (store.NodeTransition, error) {
	if a.Target.RunID == "" || a.Target.NodeID == "" {
		return store.NodeTransition{}, invalidEngineBody("transition requires a job")
	}
	change := store.NodeTransition{
		Ref: store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID},
		To:  store.NodeState(body.To), Cause: store.TransitionCause(body.Cause), Actor: engineActor(a),
		SourceAttempt: body.SourceAttempt, Extra: store.TransitionExtra{RetryAt: body.RetryAt, Publication: body.Publication},
	}
	if result := body.Result; result != nil {
		if int64(len(result.MaskedError)) > limits.ErrorBytes {
			return store.NodeTransition{}, invalidEngineBody("masked result error exceeds its byte limit")
		}
		change.Extra.Result = &store.NodeResult{
			Outcome: store.NodeState(result.Outcome), Reason: result.Reason,
			MaskedError: result.MaskedError,
			ExitCode:    result.ExitCode, OutputRecord: result.Output, ManifestRecord: result.Manifest, Failure: result.Failure,
		}
		if result.Failure != nil {
			masked, err := engineMaskedError(result.Failure.Error)
			if err != nil {
				return store.NodeTransition{}, err
			}
			if result.MaskedError != "" && result.MaskedError != masked {
				return store.NodeTransition{}, invalidEngineBody("masked result error differs from its structured failure")
			}
			change.Extra.Result.MaskedError = masked
		}
	}
	return change, nil
}

func finishForStore(a capability.Authorized, body controllerwire.FinishRequest) (store.FinishRunRequest, error) {
	masked, err := engineMaskedError(body.Error)
	if err != nil {
		return store.FinishRunRequest{}, err
	}
	if a.Target.RunID == "" {
		return store.FinishRunRequest{}, invalidEngineBody("finish requires a run")
	}
	change := store.RunTransition{
		RunID: a.Target.RunID, To: store.RunState(body.Outcome), Actor: engineActor(a),
		Cause: store.RunFinish, Reason: body.Reason, MaskedError: masked,
	}
	if a.Access.Credential == capability.CredentialPublisher {
		change.Cause = store.RunPublish
	}
	return store.FinishRunRequest{Transition: change, Unit: change.Actor.Owner}, nil
}
