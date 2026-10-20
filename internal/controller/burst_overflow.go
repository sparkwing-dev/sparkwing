package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Server) handleReadOverflow(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.store == nil {
		return missingEndpointPolicy()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	control, err := tenant.ReadOverflowControl(r.Context(), a.Principal.Authority)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.OverflowControl{MonthlyMinutes: control.MonthlyMinutes, UsedSeconds: control.UsedSeconds, PeriodStart: control.PeriodStart})
	return nil
}

func (s *Server) handleSetOverflow(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.OverflowControlWrite
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if s.store == nil {
		return missingEndpointPolicy()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	if err := tenant.SetOverflowLimit(r.Context(), a.Principal.Authority, body.MonthlyMinutes); err != nil {
		return engineLookupError(err)
	}
	return s.handleReadOverflow(w, r, a)
}
