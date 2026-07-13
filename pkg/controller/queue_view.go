package controller

import (
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// handleQueueStateView serves the controller's admission state in the same
// [wingwire.QueueState] shape the local daemon serves, so `sparkwing queue
// --profile` renders controller-arbitrated work with the one queue renderer:
// every concurrency key as a capacity row, its active holders and queued
// waiters, and each registered runner's advertised headroom.
func (s *Server) handleQueueStateView(w http.ResponseWriter, r *http.Request) {
	states, err := s.store.ListConcurrencyStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now()
	qs := wingwire.QueueState{}
	for _, st := range states {
		eff := st.EffectiveCapacity
		if eff == 0 {
			eff = st.Capacity
		}
		qs.Resources = append(qs.Resources, wingwire.ResourceState{
			Key:      st.Key,
			Capacity: float64(eff),
			Held:     float64(st.UsedCost),
		})
		for _, h := range st.Holders {
			if h.Superseded {
				continue
			}
			qs.Holders = append(qs.Holders, wingwire.Holder{
				RunID:      runNode(h.RunID, h.NodeID),
				Semaphores: []string{st.Key},
				ElapsedMS:  sinceMS(h.ClaimedAt, now),
				Origin:     wingwire.OriginController,
			})
		}
		for _, wt := range st.Waiters {
			qs.Waiters = append(qs.Waiters, wingwire.Waiter{
				RunID:     runNode(wt.RunID, wt.NodeID),
				Position:  wt.Position + 1,
				WaitingOn: []string{st.Key},
				WaitingMS: sinceMS(wt.ArrivedAt, now),
				Origin:    wingwire.OriginController,
			})
		}
	}
	for _, rh := range s.runnerHeadroom.list(now, runnerHeadroomStale) {
		qs.Runners = append(qs.Runners, wingwire.RunnerHeadroom{
			Name:        rh.Name,
			Cores:       rh.Cores,
			MemoryBytes: rh.MemoryBytes,
			QueueDepth:  rh.QueueDepth,
		})
	}
	writeJSON(w, http.StatusOK, qs)
}

// runNode joins a run id with its node id for display, or returns the run id
// alone when the work is not a per-node claim.
func runNode(runID, nodeID string) string {
	if nodeID == "" {
		return runID
	}
	return runID + "/" + nodeID
}

// sinceMS is the elapsed milliseconds from t to now, clamped at zero for a
// zero or future timestamp.
func sinceMS(t, now time.Time) int64 {
	if t.IsZero() || !t.Before(now) {
		return 0
	}
	return now.Sub(t).Milliseconds()
}
