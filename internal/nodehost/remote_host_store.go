package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// PrivateHostState supplies award-bound execution state.
type PrivateHostState interface {
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadNode(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	TransitionNode(context.Context, store.NodeCommand) (store.Applied, error)
}

// RemoteHostStore joins authenticated execution state with shared controller operations.
type RemoteHostStore struct {
	PrivateHostState
	*RemoteOwnerStore
	controller   controllerwire.Controller
	unit         string
	executing    func() bool
	LocalSecrets map[string]string
	Finish       func(context.Context, store.FinishRunRequest) (store.Applied, error)
}

// NewRemoteHostStore constructs an award-bound state adapter.
func NewRemoteHostStore(controller controllerwire.Controller, private PrivateHostState, unit string, executing func() bool) (*RemoteHostStore, error) {
	if controller == nil || private == nil || unit == "" || executing == nil {
		return nil, errors.New("host state requires an awarded controller, private execution state, unit identity and compute activity")
	}
	return &RemoteHostStore{
		PrivateHostState: private, RemoteOwnerStore: &RemoteOwnerStore{Controller: controller},
		controller: controller, unit: unit, executing: executing,
	}, nil
}

func (s *RemoteHostStore) AcceptPlan(ctx context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
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

func (s *RemoteHostStore) RecordStep(ctx context.Context, report store.StepReport) error {
	controller := s.controller
	if nodes, ok := s.PrivateHostState.(interface {
		NodeController(context.Context, controllerwire.NodeRef) (controllerwire.Controller, error)
	}); ok {
		var err error
		controller, err = nodes.NodeController(ctx, controllerwire.NodeRef{RunID: runspec.RunID(report.Attempt.RunID), NodeID: runspec.NodeID(report.Attempt.NodeID)})
		if err != nil {
			return err
		}
	}
	return controller.RecordStep(ctx, report.Attempt.RunID, report.Attempt.NodeID, controllerwire.StepReportRequest{
		StepID: report.StepID, State: report.State, SourceAttempt: report.Attempt.Attempt,
		MaskedError: report.MaskedError, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt,
	})
}

func (s *RemoteHostStore) FinishRunAndUnit(ctx context.Context, request store.FinishRunRequest) (store.Applied, error) {
	change := request.Transition
	if request.Unit != change.Actor.Owner {
		return store.Applied{}, errors.New("host finish requires its owner")
	}
	if s.Finish != nil {
		return s.Finish(ctx, request)
	}
	if change.Cause != store.RunFinish {
		return store.Applied{}, errors.New("host finish requires a supported terminal cause")
	}
	body := controllerwire.FinishRequest{Reason: change.Reason}
	if change.MaskedError != "" {
		body.Error = &apierr.Error{Code: apierr.Internal, Message: change.MaskedError}
	}
	response, err := s.controller.Finish(ctx, string(change.RunID), body)
	if err != nil {
		return store.Applied{}, err
	}
	return store.Applied{
		Outcome: store.RunState(response.Outcome), Reason: response.Reason,
		Replayed: response.Replayed, CancelRequested: response.CancelRequested, StateSeq: response.StateSeq,
		Generation: runspec.ClaimGeneration(response.Generation), Attempt: runspec.AttemptOrdinal(response.Attempt),
	}, nil
}

func (s *RemoteHostStore) RenewOwner(ctx context.Context, _ store.Actor, executing bool) (store.LeaseBeat, error) {
	return s.renew(ctx, executing)
}

func (s *RemoteHostStore) RenewNodeClaim(ctx context.Context, ref controllerwire.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	if nodes, ok := s.PrivateHostState.(interface {
		RenewNode(context.Context, controllerwire.NodeRef, store.Actor, bool) (store.LeaseBeat, error)
	}); ok {
		// The owner can be computing while its node is still acquiring a source.
		row, err := s.PrivateHostState.ReadNode(ctx, ref)
		if err != nil {
			return store.LeaseBeat{}, err
		}
		return nodes.RenewNode(ctx, ref, actor, row.Status == store.StatusRunning)
	}
	return s.renew(ctx, s.executing())
}

func (s *RemoteHostStore) renew(ctx context.Context, executing bool) (store.LeaseBeat, error) {
	request := controllerwire.RenewalRequest{Executing: executing, LocalSecrets: s.LocalSecrets}
	response, err := s.controller.Renew(ctx, s.unit, request)
	if err != nil {
		return store.LeaseBeat{}, err
	}
	return store.LeaseBeat{
		ExpiresAt: response.ExpiresAt, CancelRequested: response.CancelRequested, CancelReason: response.CancelReason,
		Watched: response.Watched, WatchedNodes: response.WatchedJobs,
	}, nil
}

var _ NativeHostStore = (*RemoteHostStore)(nil)
