package nodehost

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

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
	result := store.ScheduleSnapshot{CancelRequested: wire.CancelRequested, SelectionSkips: maps.Clone(wire.SelectionSkips)}
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
