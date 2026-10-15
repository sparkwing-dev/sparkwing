package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleListAttempts(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	id := authorized.Target.RunID
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runs, err := tenant.ListRunRetryTree(r.Context(), id)
	if err != nil {
		return engineLookupError(err)
	}
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	return nil
}
