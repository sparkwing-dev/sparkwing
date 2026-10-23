package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func (s *EngineController) handleSnapshot(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	snapshot, err := s.Snapshot(r.Context(), a)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, snapshot)
	return nil
}

// Snapshot takes one committed read and its shared scheduling projection. Empty
// generators survive, and no separate read can mix job state from another instant.
func (s *EngineController) Snapshot(ctx context.Context, authorized capability.Authorized) (controllerwire.ScheduleSnapshot, error) {
	tenant, err := s.runTransactions(ctx, authorized.Target.Team)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	snapshot, err := tenant.ReadOwnerSnapshot(ctx, authorized.Principal.Authority, authorized.Target.RunID)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, engineLookupError(err)
	}
	if snapshot.Run.ID != authorized.Target.RunID || string(snapshot.Run.Team) != authorized.Target.Team || snapshot.StoreNow.IsZero() {
		return controllerwire.ScheduleSnapshot{}, errors.New("invalid persisted run snapshot")
	}
	project := s.ProjectScheduling
	if project == nil {
		project = store.RunSnapshot.Scheduling
	}
	graph, err := project(snapshot)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	return schedulingResponse(snapshot, graph)
}

func schedulingResponse(snapshot store.RunSnapshot, graph store.ScheduleSnapshot) (controllerwire.ScheduleSnapshot, error) {
	expansions, err := snapshotExpansions(snapshot)
	if err != nil {
		return controllerwire.ScheduleSnapshot{}, err
	}
	nodes := make(map[string]runstate.NodeRow, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if node.Ref.RunID != runspec.RunID(snapshot.Run.ID) || node.Ref.NodeID == "" {
			return controllerwire.ScheduleSnapshot{}, errors.New("invalid persisted snapshot job")
		}
		if _, exists := nodes[string(node.Ref.NodeID)]; exists {
			return controllerwire.ScheduleSnapshot{}, errors.New("duplicate persisted snapshot job")
		}
		nodes[string(node.Ref.NodeID)] = node
	}
	out := controllerwire.ScheduleSnapshot{
		RunID: snapshot.Run.ID, StoreNow: snapshot.StoreNow, CancelRequested: graph.CancelRequested, GuardRefusal: graph.GuardRefusal,
		Jobs: make([]controllerwire.ScheduleJob, 0, len(graph.Nodes)), Generators: make([]controllerwire.ScheduleGenerator, 0, len(graph.Expansions)),
		Expansions: expansions,
	}
	for _, scheduled := range graph.Nodes {
		node, exists := nodes[scheduled.ID]
		if !exists {
			return controllerwire.ScheduleSnapshot{}, errors.New("scheduling projection names an absent job")
		}
		var declaration nodeproto.JobSpec
		if err := json.Unmarshal(node.Spec, &declaration); err != nil || declaration.ID != scheduled.ID {
			return controllerwire.ScheduleSnapshot{}, errors.New("invalid persisted snapshot declaration")
		}
		out.Jobs = append(out.Jobs, controllerwire.ScheduleJob{JobDetail: snapshotJob(node), Declaration: declaration, Ready: scheduled.Ready, Claimed: scheduled.Claimed})
		delete(nodes, scheduled.ID)
	}
	if len(nodes) != 0 {
		return controllerwire.ScheduleSnapshot{}, errors.New("scheduling projection omits persisted jobs")
	}
	for _, expansion := range graph.Expansions {
		out.Generators = append(out.Generators, controllerwire.ScheduleGenerator{
			ID: expansion.GeneratorID, SourceJob: expansion.SourceNodeID, State: expansion.State, Members: append([]string{}, expansion.Members...),
		})
	}
	return out, nil
}

func snapshotExpansions(snapshot store.RunSnapshot) ([]controllerwire.AcceptedExpansion, error) {
	out := make([]controllerwire.AcceptedExpansion, 0, len(snapshot.Expansions))
	for _, row := range snapshot.Expansions {
		if row.Source.RunID != runspec.RunID(snapshot.Run.ID) || row.Source.NodeID == "" || row.SourceGeneration <= 0 || row.BodyHash == "" || !json.Valid(row.Body) {
			return nil, errors.New("invalid persisted expansion snapshot")
		}
		out = append(out, controllerwire.AcceptedExpansion{
			SourceJob: string(row.Source.NodeID), SourceGeneration: row.SourceGeneration,
			Body: append(json.RawMessage(nil), row.Body...), BodyHash: row.BodyHash, Registration: row.Registration,
		})
	}
	return out, nil
}

func executionJob(node runstate.NodeRow) controllerwire.ExecutionJob {
	return controllerwire.ExecutionJob{
		JobDetail: snapshotJob(node), Spec: node.Spec, SpecHash: node.SpecHash, Execution: node.Execution,
		ClaimedBy: node.ClaimedBy, CapabilityPrefix: node.CapabilityPrefix, OwnerCapabilityPrefix: node.OwnerCapabilityPrefix, ReadyAt: node.ReadyAt, LeaseExpiresAt: node.LeaseExpiresAt,
		ApplicationRetries: node.ApplicationRetries, Disruptions: node.Disruptions, ExpansionDigest: node.ExpansionDigest,
		OutputRecord: node.OutputRecord, ManifestRecord: node.ManifestRecord,
	}
}

func snapshotJob(node runstate.NodeRow) controllerwire.JobDetail {
	return controllerwire.JobDetail{
		ID: string(node.Ref.NodeID), Status: string(node.Status), Outcome: string(node.Outcome), StatusDetail: node.StatusDetail,
		Generation: int64(node.Generation), Attempt: int64(node.Attempt), StateSeq: node.StateSeq, Expansion: string(node.Expansion),
		StartedAt: node.StartedAt, FinishedAt: node.FinishedAt, CancelRequestedAt: node.CancelRequestedAt,
	}
}
