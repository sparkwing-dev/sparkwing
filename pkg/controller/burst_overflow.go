package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Server) handleReadOverflow(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.store == nil {
		return missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
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
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	if err := tenant.SetOverflowLimit(r.Context(), a.Principal.Authority, body.MonthlyMinutes); err != nil {
		return engineLookupError(err)
	}
	return s.handleReadOverflow(w, r, a)
}

func (s *Server) handleRequestMachineAutomation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.MachineAutomationRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Requested == nil {
		return invalidEngineBody("requested must be a boolean")
	}
	tenant, err := s.machineAutomationTenant(r, a)
	if err != nil {
		return err
	}
	state, err := tenant.RequestMachineAutomation(r.Context(), a.Principal.Authority, r.PathValue("runner"), *body.Requested)
	return writeMachineAutomation(w, r, state, err)
}

func (s *Server) handleApproveMachineAutomation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.MachineAutomationApproval
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Approved == nil {
		return invalidEngineBody("approved must be a boolean")
	}
	tenant, err := s.machineAutomationTenant(r, a)
	if err != nil {
		return err
	}
	state, err := tenant.ApproveMachineAutomation(r.Context(), a, r.PathValue("runner"), *body.Approved)
	return writeMachineAutomation(w, r, state, err)
}

func (s *Server) machineAutomationTenant(r *http.Request, a capability.Authorized) (*store.Tenant, error) {
	if s.store == nil {
		return nil, missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return nil, engineLookupError(err)
	}
	return tenant, nil
}

func writeMachineAutomation(w http.ResponseWriter, r *http.Request, state string, err error) error {
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.MachineAutomation{Runner: r.PathValue("runner"), State: state})
	return nil
}
