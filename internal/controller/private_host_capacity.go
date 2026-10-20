package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
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
	result, err := tenant.AdmitCapacity(r.Context(), body.Binding, engineActor(a))
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
