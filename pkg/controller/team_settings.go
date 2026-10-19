package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Server) handleTeamSettings(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	if r.Method == http.MethodPut {
		var request controllerwire.TeamSettingsUpdate
		if err := decodeEngineRequest(r, &request); err != nil {
			return err
		}
		if request.MembersCanAddMachines == nil && request.ClaimRanking == nil {
			return invalidEngineBody("set members_can_add_machines or claim_ranking")
		}
		if err := tenant.SetAuthorizedTeamSettings(r.Context(), a, request); err != nil {
			return accountMutationError(err)
		}
	}
	settings, err := tenant.TeamSettings(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, settings)
	return nil
}
