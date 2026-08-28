package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
