package controller

import (
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
		if !s.deployment.Hosted() && request.ClaimRanking != nil && slices.Contains(*request.ClaimRanking, store.TierCloud) {
			return invalidEngineBody("cloud is not a runner tier on a self-hosted controller, which has no Cloud launcher")
		}
		if err := tenant.SetAuthorizedTeamSettings(r.Context(), a, request); err != nil {
			return accountMutationError(err)
		}
	}
	settings, err := tenant.TeamSettings(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	if !s.deployment.Hosted() {
		settings.ClaimRanking = slices.DeleteFunc(settings.ClaimRanking, func(tier string) bool { return tier == store.TierCloud })
	}
	writeJSON(w, http.StatusOK, settings)
	return nil
}
