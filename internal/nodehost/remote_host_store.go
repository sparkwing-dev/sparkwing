package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// PrivateHostState supplies award-bound execution state.
type PrivateHostState interface {
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadNode(context.Context, store.NodeRef) (store.NodeRow, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
}

// RemoteHostStore joins authenticated execution state with shared controller operations.
type RemoteHostStore struct {
	PrivateHostState
	*RemoteOwnerStore
	controller   controllerwire.Controller
	unit         string
	executing    func() bool
	LocalSecrets map[string]string
	Available    func(context.Context) (*match.Resources, error)
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

// ReadSnapshot refreshes burst availability before reading canonical placement.
func (s *RemoteHostStore) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	if s.Available != nil {
		if _, err := s.renew(ctx, s.executing()); err != nil {
			return store.RunSnapshot{}, err
		}
	}
	return s.PrivateHostState.ReadSnapshot(ctx, run)
}

// ReconcileNode refreshes burst availability before a self-claim.
func (s *RemoteHostStore) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	if s.Available != nil {
		if _, err := s.renew(ctx, s.executing()); err != nil {
			return store.ReconciledNode{}, err
		}
	}
	return s.RemoteOwnerStore.ReconcileNode(ctx, request)
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
		NodeController(context.Context, store.NodeRef) (controllerwire.Controller, error)
	}); ok {
		var err error
		controller, err = nodes.NodeController(ctx, store.NodeRef{RunID: report.Attempt.RunID, NodeID: report.Attempt.NodeID})
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
	if request.Unit != change.Actor.Owner ||
		(change.To != store.RunSuccess && change.To != store.RunFailed && change.To != store.RunCancelled) {
		return store.Applied{}, errors.New("host finish requires its owner and terminal verdict")
	}
	if s.Finish != nil {
		return s.Finish(ctx, request)
	}
	if change.Cause != store.RunFinish {
		return store.Applied{}, errors.New("host finish requires a supported terminal cause")
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

func (s *RemoteHostStore) RenewOwner(ctx context.Context, _ store.Actor, executing bool) (store.LeaseBeat, error) {
	return s.renew(ctx, executing)
}

func (s *RemoteHostStore) RenewNodeClaim(ctx context.Context, ref store.NodeRef, actor store.Actor) (store.LeaseBeat, error) {
	if nodes, ok := s.PrivateHostState.(interface {
		RenewNode(context.Context, store.NodeRef, store.Actor, bool) (store.LeaseBeat, error)
	}); ok {
		return nodes.RenewNode(ctx, ref, actor, s.executing())
	}
	return s.renew(ctx, s.executing())
}

func (s *RemoteHostStore) renew(ctx context.Context, executing bool) (store.LeaseBeat, error) {
	request := controllerwire.RenewalRequest{Executing: executing, LocalSecrets: s.LocalSecrets}
	if s.Available != nil {
		var err error
		request.Available, err = s.Available(ctx)
		if err != nil {
			return store.LeaseBeat{}, err
		}
	}
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
