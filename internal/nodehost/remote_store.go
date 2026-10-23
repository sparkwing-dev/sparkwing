package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// RemoteOwnerStore projects shared controller responses into the same owner
// interfaces as a local Tenant. It never introduces another decision policy.
type RemoteOwnerStore struct{ Controller controllerwire.Controller }

func (s *RemoteOwnerStore) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	if s.Controller == nil || request.Ref.RunID == "" || request.Ref.NodeID == "" {
		return store.ReconciledNode{}, errors.New("reconciliation requires a controller and job identity")
	}
	body := controllerwire.ReconcileRequest{Policy: string(request.Policy), MaterializeOnly: request.MaterializeOnly}
	if request.Source != nil {
		if request.Policy != request.Source.Kind {
			return store.ReconciledNode{}, errors.New("reconciliation source policy differs from its read edge")
		}
		body.Source = &controllerwire.ReuseSource{Policy: string(request.Source.Kind), Attempt: request.Source.Source, Record: request.Source.Record}
		if result := request.Source.Result; result != nil {
			body.Source.Result = &controllerwire.MaterializedResultSource{RunID: string(result.Ref.RunID), JobID: string(result.Ref.NodeID), StateSeq: result.StateSeq}
		}
	}
	wire, err := s.Controller.ReconcileJob(ctx, string(request.Ref.RunID), string(request.Ref.NodeID), body)
	if err != nil {
		return store.ReconciledNode{}, err
	}
	row, err := executionRow(string(request.Ref.RunID), wire.Job)
	if err != nil || row.Ref != request.Ref {
		return store.ReconciledNode{}, errors.Join(err, errors.New("reconciliation response changed its job"))
	}
	result := store.ReconciledNode{Kind: store.ReconcileKind(wire.Kind), Node: row}
	if wire.Source != nil {
		result.Source = &store.MaterializeSource{Kind: store.SourcePolicy(wire.Source.Policy), Source: wire.Source.Attempt, Record: wire.Source.Record}
		if source := wire.Source.Result; source != nil {
			result.Source.Result = &store.MaterializedResultSource{Ref: controllerwire.NodeRef{RunID: runspec.RunID(source.RunID), NodeID: runspec.NodeID(source.JobID)}, StateSeq: source.StateSeq}
		}
	}
	if result.Kind == store.Execute {
		claim := wire.Claim
		if request.MaterializeOnly || claim == nil || claim.Unit != capability.UnitNode || runspec.RunID(claim.RunID) != request.Ref.RunID || runspec.NodeID(claim.JobID) != request.Ref.NodeID || claim.Capability == "" || claim.Binding.Kind != capability.BindingNode || runspec.RunID(claim.Binding.RunID) != request.Ref.RunID || runspec.NodeID(claim.Binding.NodeID) != request.Ref.NodeID || claim.Binding.Holder != row.ClaimedBy || runspec.ClaimGeneration(claim.Binding.Generation) != row.Generation || runspec.AttemptOrdinal(claim.Binding.Attempt) != row.Attempt {
			return store.ReconciledNode{}, errors.New("reconciliation returned an inconsistent execution award")
		}
		result.Claim = &store.ClaimAward{Holder: claim.Binding.Holder, Generation: runspec.ClaimGeneration(claim.Binding.Generation), CapabilityPrefix: row.CapabilityPrefix, LeaseExpiresAt: claim.LeaseExpiresAt}
		result.Bearer = []byte(claim.Capability)
		if !reconciledClaimMatches(result) {
			return store.ReconciledNode{}, errors.New("execution award differs from its persisted claim")
		}
	} else if wire.Claim != nil {
		return store.ReconciledNode{}, errors.New("nonexecuting reconciliation returned an award")
	}
	switch result.Kind {
	case store.Execute, store.WaitHolder:
	case store.KeepTerminal, store.Materialize:
		if row.Status != store.StatusDone || row.Outcome == "" {
			return store.ReconciledNode{}, errors.New("reconciliation returned an unfinished result")
		}
	case store.Dispatch:
		if row.Status != store.StatusPending || row.Outcome != "" || row.ClaimedBy != "" || !request.MaterializeOnly && row.ReadyAt == nil {
			return store.ReconciledNode{}, errors.New("reconciliation returned an invalid dispatch miss")
		}
	default:
		return store.ReconciledNode{}, errors.New("reconciliation returned an unknown decision")
	}
	return result, nil
}

func (s *RemoteOwnerStore) AcceptExpansion(ctx context.Context, submission store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	if s.Controller == nil || submission.Source.RunID == "" || submission.Source.NodeID == "" || submission.SourceGeneration < 0 || (len(submission.Body) == 0) == (submission.Failure == nil) {
		return store.AcceptedExpansion{}, errors.New("expansion requires a bound source and body or failure")
	}
	body := controllerwire.ExpansionSubmission{SourceGeneration: int64(submission.SourceGeneration), Body: slices.Clone(submission.Body), Registration: submission.Registration}
	if submission.Failure != nil {
		body.Failure = &apierr.Error{Code: submission.Failure.Code, Message: submission.Failure.MaskedError}
	}
	wire, err := s.Controller.AcceptExpansion(ctx, string(submission.Source.RunID), string(submission.Source.NodeID), body)
	if err != nil {
		return store.AcceptedExpansion{}, err
	}
	if runspec.ClaimGeneration(wire.SourceGeneration) != submission.SourceGeneration || wire.State != string(store.ExpansionAccepted) && wire.State != string(store.ExpansionFailed) {
		return store.AcceptedExpansion{}, errors.New("expansion response changed its source generation or resolution")
	}
	result := store.AcceptedExpansion{Source: submission.Source, SourceGeneration: runspec.ClaimGeneration(wire.SourceGeneration), State: store.ExpansionState(wire.State), BodyHash: wire.BodyHash, Replayed: wire.Replayed}
	seen := map[string]bool{}
	for _, job := range wire.Jobs {
		row, err := executionRow(string(submission.Source.RunID), job.Current)
		var declaration nodeproto.JobSpec
		if err != nil || json.Unmarshal(row.Spec, &declaration) != nil || !reflect.DeepEqual(job.Job, declaration) || row.SpecHash != job.SpecHash || !reflect.DeepEqual(row.Execution, job.Execution) || seen[string(row.Ref.NodeID)] {
			return store.AcceptedExpansion{}, errors.New("expansion response differs from its persisted generated job")
		}
		seen[string(row.Ref.NodeID)] = true
		result.Nodes = append(result.Nodes, row)
	}
	if wire.State == string(store.ExpansionFailed) && len(result.Nodes) != 0 {
		return store.AcceptedExpansion{}, errors.New("failed expansion returned generated jobs")
	}
	return result, nil
}

func executionRow(runID string, job controllerwire.ExecutionJob) (store.NodeRow, error) {
	var declaration nodeproto.JobSpec
	if job.ID == "" || job.SpecHash == "" || job.Generation < 0 || job.Attempt < 0 || job.ApplicationRetries < 0 || job.Disruptions < 0 || json.Unmarshal(job.Spec, &declaration) != nil || declaration.ID != job.ID {
		return store.NodeRow{}, errors.New("execution response has an invalid persisted job")
	}
	return store.NodeRow{
		Ref: controllerwire.NodeRef{RunID: runspec.RunID(runID), NodeID: runspec.NodeID(job.ID)}, Status: store.NodeStatus(job.Status), Outcome: store.NodeState(job.Outcome), StatusDetail: job.StatusDetail,
		ClaimedBy: job.ClaimedBy, CapabilityPrefix: job.CapabilityPrefix, OwnerCapabilityPrefix: job.OwnerCapabilityPrefix, Generation: runspec.ClaimGeneration(job.Generation), Attempt: runspec.AttemptOrdinal(job.Attempt), ApplicationRetries: job.ApplicationRetries, Disruptions: job.Disruptions,
		ReadyAt: job.ReadyAt, LeaseExpiresAt: job.LeaseExpiresAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt, CancelRequestedAt: job.CancelRequestedAt,
		OutputRecord: job.OutputRecord, ManifestRecord: job.ManifestRecord,
		StateSeq: job.StateSeq, Expansion: store.ExpansionState(job.Expansion), ExpansionDigest: job.ExpansionDigest, Spec: slices.Clone(job.Spec), SpecHash: job.SpecHash, Execution: job.Execution,
	}, nil
}

// schedule reconstructs only the store's scheduling projection; the owner calls
// the same Decide used locally. HTTP never serializes Actor or caller authority.
func (s *RemoteOwnerStore) schedule(ctx context.Context, runID string) (store.ScheduleSnapshot, error) {
	if s.Controller == nil {
		return store.ScheduleSnapshot{}, errors.New("scheduling requires an authenticated controller")
	}
	wire, err := s.Controller.ReadSnapshot(ctx, runID)
	if err != nil {
		return store.ScheduleSnapshot{}, err
	}
	if wire.RunID != runID {
		return store.ScheduleSnapshot{}, errors.New("scheduling response changed the run identity")
	}
	result := store.ScheduleSnapshot{CancelRequested: wire.CancelRequested}
	for _, job := range wire.Jobs {
		if job.ID == "" || job.ID != job.Declaration.ID {
			return store.ScheduleSnapshot{}, errors.New("scheduling response changed a job identity")
		}
		kind := "work"
		if job.Declaration.Approval != nil {
			kind = "approval"
		}
		result.Nodes = append(result.Nodes, store.ScheduleNode{
			ID: job.ID, Kind: kind, Status: job.Status, Outcome: job.Outcome,
			Generation: job.Generation, Ready: job.Ready, Claimed: job.Claimed,
			Deps: slices.Clone(job.Declaration.Needs), DepsIfPresent: slices.Clone(job.Declaration.NeedsIfPresent),
			NeedsGenerated: slices.Clone(job.Declaration.NeedsGenerated), OnFailureOf: job.Declaration.OnFailureOf,
			ContinueOnError: job.Declaration.ContinueOnError, Optional: job.Declaration.Optional, Expansion: job.Expansion,
		})
	}
	for _, generator := range wire.Generators {
		result.Expansions = append(result.Expansions, store.ScheduleExpansion{
			GeneratorID: generator.ID, SourceNodeID: generator.SourceJob,
			State: generator.State, Members: slices.Clone(generator.Members),
		})
	}
	return result, nil
}

var (
	_ reconciler     = (*RemoteOwnerStore)(nil)
	_ expansionStore = (*RemoteOwnerStore)(nil)
)
