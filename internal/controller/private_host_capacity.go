package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) localHostCapacity(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalCapacityRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Binding.RunID != a.Target.RunID {
		return localHostRefusal()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	result, err := tenant.MayStart(r.Context(), body.Binding, engineActor(a))
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Server) localHostRelinquishStart(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalCapacityRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Binding.RunID != a.Target.RunID {
		return localHostRefusal()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	if err := tenant.RelinquishStart(r.Context(), body.Binding, engineActor(a)); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) localHostReleaseEvaluation(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body localhostwire.LocalEvaluationRelease
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	result, err := tenant.ReleaseEvaluation(r.Context(), controllerwire.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(body.NodeID)}, engineActor(a))
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
