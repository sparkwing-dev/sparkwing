package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	runs, err := tenant.ListRunRetryTree(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}
