package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) localHostDataAuthority(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	authority, err := tenant.ResolveDataAuthority(r.Context(), a)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, localhostwire.LocalDataAuthority{Authority: authority})
	return nil
}
