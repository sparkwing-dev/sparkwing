package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// handleGetRunReceipt computes the IMP-016 receipt for one run on
// demand from the run + nodes rows. The full receipt JSON is not
// stored; the queryable receipt_sha + cost_* columns hold the small
// summary. Recompute is the canonical path so the receipt always
// reflects current store contents (post-replay, post-retry, etc.).
func (s *Server) handleGetRunReceipt(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	run, err := s.store.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	nodes, err := s.store.ListNodes(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	rec := receipt.BuildReceipt(run, nodes, s.costPerRunnerHour, s.costRateSource)
	writeJSON(w, http.StatusOK, rec)
}
