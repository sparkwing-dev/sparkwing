package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
)

func (s *Server) handleGetRunReceipt(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runID := authorized.Target.RunID
	run, err := tenant.GetRun(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	nodes, err := tenant.ListNodes(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	rec := receipt.BuildReceipt(run, nodes, s.costPerRunnerHour, s.costRateSource)
	writeJSON(w, http.StatusOK, rec)
	return nil
}
