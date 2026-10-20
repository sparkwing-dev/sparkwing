package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *EngineController) readRunSnapshot(ctx context.Context, a capability.Authorized) (store.RunSnapshot, error) {
	tenant, err := s.runTransactions(ctx, a.Target.Team)
	if err != nil {
		return store.RunSnapshot{}, err
	}
	snapshot, err := tenant.ReadSnapshot(ctx, a.Target.RunID)
	if err != nil {
		return store.RunSnapshot{}, engineLookupError(err)
	}
	if snapshot.Run.ID != a.Target.RunID || string(snapshot.Run.Team) != a.Target.Team || snapshot.StoreNow.IsZero() {
		return store.RunSnapshot{}, errors.New("invalid persisted run snapshot")
	}
	return snapshot, nil
}

func (s *EngineController) handleJobs(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	snapshot, err := s.readRunSnapshot(r.Context(), a)
	if err != nil {
		return err
	}
	jobs := make([]controllerwire.JobDetail, 0, len(snapshot.Nodes))
	seen := make(map[string]bool, len(snapshot.Nodes))
	for _, row := range snapshot.Nodes {
		if row.Ref.RunID != snapshot.Run.ID || row.Ref.NodeID == "" || seen[row.Ref.NodeID] {
			return errors.New("invalid persisted job snapshot")
		}
		seen[row.Ref.NodeID] = true
		jobs = append(jobs, snapshotJob(row))
	}
	writeJSON(w, http.StatusOK, jobs)
	return nil
}

func (s *EngineController) handleJob(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	read := s.ReadRunStatus
	if read == nil {
		if s.Store == nil {
			return missingEndpointPolicy()
		}
		read = func(ctx context.Context, team store.Team, run string) (RunStatusFacts, error) {
			tenant, err := s.tenantForTeam(ctx, team)
			if err != nil {
				return RunStatusFacts{}, err
			}
			view, err := tenant.ReadRunView(ctx, run)
			return RunStatusFacts{Snapshot: view.Snapshot, Attempts: view.Attempts}, err
		}
	}
	facts, err := read(r.Context(), store.Team(a.Target.Team), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	result, err := debugJobResponse(a.Target, store.RunView{Snapshot: facts.Snapshot, Attempts: facts.Attempts})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func debugJobResponse(target capability.Target, view store.RunView) (controllerwire.DebugJob, error) {
	run := view.Snapshot.Run
	if run.ID != target.RunID || string(run.Team) != target.Team || view.Snapshot.StoreNow.IsZero() {
		return controllerwire.DebugJob{}, errors.New("invalid persisted debug run")
	}
	var selected *store.NodeRow
	for i := range view.Snapshot.Nodes {
		row := &view.Snapshot.Nodes[i]
		if row.Ref.RunID != run.ID || row.Ref.NodeID == "" {
			return controllerwire.DebugJob{}, errors.New("invalid persisted debug job")
		}
		if row.Ref.NodeID == target.NodeID {
			if selected != nil {
				return controllerwire.DebugJob{}, errors.New("duplicate persisted debug job")
			}
			selected = row
		}
	}
	if selected == nil {
		return controllerwire.DebugJob{}, engineLookupError(store.ErrNotFound)
	}
	result := controllerwire.DebugJob{RunID: run.ID, Number: run.Number, Job: snapshotJob(*selected)}
	if selected.Status == store.StatusRunning && selected.LeaseExpiresAt != nil && selected.LeaseExpiresAt.After(view.Snapshot.StoreNow) {
		found := false
		for _, attempt := range view.Attempts {
			id := attempt.Identity
			if id.Team != target.Team || id.RunID != run.ID || id.NodeID != target.NodeID || id.Attempt != selected.Attempt || id.Generation != selected.Generation || attempt.FinishedAt != nil {
				continue
			}
			if found || attempt.CapabilityPrefix != selected.CapabilityPrefix || attempt.Holder != selected.ClaimedBy {
				return controllerwire.DebugJob{}, errors.New("invalid current debug attempt")
			}
			found = true
			kind := attempt.ExecutorKind
			if kind == "coordinator" && attempt.ExecutorLocation == "local" {
				kind = "local"
			} else if kind == "agent" {
				kind = "runner"
			}
			if kind == "local" || kind == "runner" {
				result.Execution = &controllerwire.DebugExecution{Kind: kind, Runner: attempt.ExecutorName}
			}
		}
		if !found {
			return controllerwire.DebugJob{}, errors.New("missing current debug attempt")
		}
	}
	if err := result.Validate(target.RunID, target.NodeID); err != nil {
		return controllerwire.DebugJob{}, err
	}
	return result, nil
}
