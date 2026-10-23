package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

// PrivateHostState supplies award-bound execution state.
type PrivateHostState interface {
	MayStart(context.Context, runstate.CapacityBinding, runstate.Actor) (runstate.AcquireSlotResponse, error)
	ReleaseEvaluation(context.Context, runspec.NodeRef, runstate.Actor) (runstate.Applied, error)
	RelinquishStart(context.Context, runstate.CapacityBinding, runstate.Actor) error
	ReadSnapshot(context.Context, string) (runstate.RunSnapshot, error)
	ReadNode(context.Context, runspec.NodeRef) (runstate.NodeRow, error)
	TransitionRun(context.Context, runstate.RunTransition) (runstate.Applied, error)
	TransitionNode(context.Context, runspec.NodeRef, runstate.Actor, runstate.NodeCommandPayload) (runstate.Applied, error)
}

// RemoteHostStore joins authenticated execution state with shared controller operations.
type RemoteHostStore struct {
	PrivateHostState
	*RemoteOwnerStore
	controller   controllerwire.Controller
	unit         string
	executing    func() bool
	LocalSecrets map[string]string
	Finish       func(context.Context, runstate.FinishRunRequest) (runstate.Applied, error)
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
		NodeController(context.Context, runspec.NodeRef) (controllerwire.Controller, error)
	}); ok {
		var err error
		controller, err = nodes.NodeController(ctx, runspec.NodeRef{RunID: runspec.RunID(report.Attempt.RunID), NodeID: runspec.NodeID(report.Attempt.NodeID)})
		if err != nil {
			return err
		}
	}
	return controller.RecordStep(ctx, report.Attempt.RunID, report.Attempt.NodeID, controllerwire.StepReportRequest{
		StepID: report.StepID, State: report.State, SourceAttempt: report.Attempt.Attempt,
		MaskedError: report.MaskedError, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt,
	})
}

func (s *RemoteHostStore) FinishRunAndUnit(ctx context.Context, request runstate.FinishRunRequest) (runstate.Applied, error) {
	change := request.Transition
	if request.Unit != change.Actor.Owner {
		return runstate.Applied{}, errors.New("host finish requires its owner")
	}
	if s.Finish != nil {
		return s.Finish(ctx, request)
	}
	if change.Cause != runstate.RunFinish {
		return runstate.Applied{}, errors.New("host finish requires a supported terminal cause")
	}
	body := controllerwire.FinishRequest{Reason: change.Reason}
	if change.MaskedError != "" {
		body.Error = &apierr.Error{Code: apierr.Internal, Message: change.MaskedError}
	}
	response, err := s.controller.Finish(ctx, string(change.RunID), body)
	if err != nil {
		return runstate.Applied{}, err
	}
	return runstate.Applied{
		Outcome: runstate.RunState(response.Outcome), Reason: response.Reason,
		Replayed: response.Replayed, CancelRequested: response.CancelRequested, StateSeq: response.StateSeq,
		Generation: runspec.ClaimGeneration(response.Generation), Attempt: runspec.AttemptOrdinal(response.Attempt),
	}, nil
}

func (s *RemoteHostStore) RenewOwner(ctx context.Context, _ runstate.Actor, executing bool) (runstate.LeaseBeat, error) {
	return s.renew(ctx, executing)
}

func (s *RemoteHostStore) RenewNodeClaim(ctx context.Context, ref runspec.NodeRef, actor runstate.Actor) (runstate.LeaseBeat, error) {
	if nodes, ok := s.PrivateHostState.(interface {
		RenewNode(context.Context, runspec.NodeRef, runstate.Actor, bool) (runstate.LeaseBeat, error)
	}); ok {
		// The owner can be computing while its node is still acquiring a source.
		row, err := s.PrivateHostState.ReadNode(ctx, ref)
		if err != nil {
			return runstate.LeaseBeat{}, err
		}
		return nodes.RenewNode(ctx, ref, actor, row.Status == runstate.StatusRunning)
	}
	return s.renew(ctx, s.executing())
}

func (s *RemoteHostStore) renew(ctx context.Context, executing bool) (runstate.LeaseBeat, error) {
	request := controllerwire.RenewalRequest{Executing: executing, LocalSecrets: s.LocalSecrets}
	response, err := s.controller.Renew(ctx, s.unit, request)
	if err != nil {
		return runstate.LeaseBeat{}, err
	}
	return runstate.LeaseBeat{
		ExpiresAt: response.ExpiresAt, CancelRequested: response.CancelRequested, CancelReason: response.CancelReason,
		Watched: response.Watched, WatchedNodes: response.WatchedJobs,
	}, nil
}

var _ NativeHostStore = (*RemoteHostStore)(nil)

func (s *RemoteHostStore) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	snapshot, err := s.PrivateHostState.ReadSnapshot(ctx, run)
	row := snapshot.Run
	return store.RunSnapshot{Run: store.RunRow{
		ID:                row.ID,
		Number:            row.Number,
		Team:              store.Team(row.Team),
		Pipeline:          row.Pipeline,
		State:             row.State,
		Request:           row.Request,
		Lineage:           row.Lineage,
		Retry:             row.Retry,
		Spec:              row.Spec,
		Plan:              row.Plan,
		PlanHash:          row.PlanHash,
		Dispatch:          row.Dispatch,
		AllowedPools:      row.AllowedPools,
		Owner:             row.Owner,
		CreatedAt:         row.CreatedAt,
		CancelRequestedAt: row.CancelRequestedAt,
		CancelReason:      row.CancelReason,
		StartedAt:         row.StartedAt,
		FinishedAt:        row.FinishedAt,
		StateSeq:          row.StateSeq,
		StateDigest:       row.StateDigest,
		StateActor:        row.StateActor,
	}, Nodes: snapshot.Nodes, Expansions: snapshot.Expansions, StoreNow: snapshot.StoreNow}, err
}

func (s *RemoteHostStore) TransitionNode(ctx context.Context, command store.NodeCommand) (runstate.Applied, error) {
	ref, actor := store.NodeCommandAuthority(command)
	payload, err := store.NodeCommandPayloadFor(command)
	if err != nil {
		return runstate.Applied{}, err
	}
	return s.PrivateHostState.TransitionNode(ctx, ref, actor, runstate.NodeCommandPayload(payload))
}
