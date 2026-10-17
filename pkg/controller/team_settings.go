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
		var request struct {
			MembersCanAddMachines *bool `json:"members_can_add_machines"`
		}
		if err := decodeEngineRequest(r, &request); err != nil {
			return err
		}
		if request.MembersCanAddMachines == nil {
			return invalidEngineBody("members_can_add_machines must be a boolean")
		}
		if err := tenant.SetAuthorizedTeamSettings(r.Context(), a, controllerwire.TeamSettings{MembersCanAddMachines: *request.MembersCanAddMachines}); err != nil {
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
