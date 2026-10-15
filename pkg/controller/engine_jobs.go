package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	tenant, err := s.runTransactions(r.Context(), a.Target.Team)
	if err != nil {
		return err
	}
	ref := store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}
	row, err := tenant.ReadNode(r.Context(), ref)
	if err != nil {
		return engineLookupError(err)
	}
	if row.Ref != ref {
		return errors.New("invalid persisted job")
	}
	writeJSON(w, http.StatusOK, snapshotJob(row))
	return nil
}
