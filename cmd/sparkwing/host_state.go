package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type privateHostState interface {
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadNode(context.Context, store.NodeRef) (store.NodeRow, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
}

type cliHostState struct {
	privateHostState
	*nodehost.RemoteOwnerStore
	controller controllerwire.Controller
	unit       string
	executing  func() bool
}

func newCLIHostState(controller controllerwire.Controller, private privateHostState, unit string, executing func() bool) (*cliHostState, error) {
	if controller == nil || private == nil || unit == "" || executing == nil {
		return nil, errors.New("host state requires an awarded controller, private execution state, unit identity and compute activity")
	}
	return &cliHostState{
		privateHostState: private, RemoteOwnerStore: &nodehost.RemoteOwnerStore{Controller: controller},
		controller: controller, unit: unit, executing: executing,
	}, nil
}

func (s *cliHostState) AcceptPlan(ctx context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
	response, err := s.controller.AcceptPlan(ctx, request.RunID, controllerwire.PlanSubmission{
		Declarations: request.Seal.Declarations, Resolved: request.Seal.Resolved, Plan: request.Seal.Plan,
	})
	if err != nil {
		return store.AcceptedPlan{}, err
	}
	return store.AcceptedPlan{
		Spec: response.Spec, Plan: response.Plan, PlanHash: string(response.PlanHash),
		Replayed: response.Replayed,
	}, nil
}

func (s *cliHostState) RecordStep(ctx context.Context, report store.StepReport) error {
	return s.controller.RecordStep(ctx, report.Attempt.RunID, report.Attempt.NodeID, controllerwire.StepReportRequest{
		StepID: report.StepID, State: report.State, SourceAttempt: report.Attempt.Attempt,
		MaskedError: report.MaskedError, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt,
	})
}

func (s *cliHostState) FinishRunAndUnit(ctx context.Context, request store.FinishRunRequest) (store.Applied, error) {
	change := request.Transition
	if request.Unit != change.Actor.Owner || change.Cause != store.RunFinish ||
		(change.To != store.RunSuccess && change.To != store.RunFailed && change.To != store.RunCancelled) {
		return store.Applied{}, errors.New("host finish requires its owner and terminal verdict")
	}
	body := controllerwire.FinishRequest{Outcome: string(change.To), Reason: change.Reason}
	if change.MaskedError != "" {
		body.Error = &apierr.Error{Code: apierr.Internal, Message: change.MaskedError}
	}
	response, err := s.controller.Finish(ctx, change.RunID, body)
	if err != nil {
		return store.Applied{}, err
	}
	return store.Applied{
		Replayed: response.Replayed, CancelRequested: response.CancelRequested, StateSeq: response.StateSeq,
		Generation: response.Generation, Attempt: response.Attempt,
	}, nil
}

func (s *cliHostState) RenewOwner(ctx context.Context, _ store.Actor, executing bool) (store.LeaseBeat, error) {
	return s.renew(ctx, executing)
}

func (s *cliHostState) RenewNodeClaim(ctx context.Context, _ store.NodeRef, _ store.Actor) (store.LeaseBeat, error) {
	return s.renew(ctx, s.executing())
}

func (s *cliHostState) renew(ctx context.Context, executing bool) (store.LeaseBeat, error) {
	response, err := s.controller.Renew(ctx, s.unit, controllerwire.RenewalRequest{Executing: executing})
	if err != nil {
		return store.LeaseBeat{}, err
	}
	return store.LeaseBeat{
		ExpiresAt: response.ExpiresAt, CancelRequested: response.CancelRequested, CancelReason: response.CancelReason,
		Watched: response.Watched, WatchedNodes: response.WatchedJobs,
	}, nil
}

var _ nodehost.NativeHostStore = (*cliHostState)(nil)
