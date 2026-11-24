package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func (s *Server) handleOperatorCreateTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request controllerwire.OperatorTeamCreate
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if s.operator == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	issued, err := s.operator.CreateOperatorTeam(r.Context(), a, request.Slug, request.DisplayName)
	if errors.Is(err, store.ErrSlugTaken) {
		return &apierr.Error{Code: apierr.Conflict, Message: "team already exists or its slug is retired"}
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusCreated, operatorTeamAdmin(issued))
	return nil
}

func (s *Server) handleOperatorReissueTeamAdmin(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request controllerwire.OperatorTeamAdminReissue
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if s.operator == nil {
		return routepolicy.MissingEndpointPolicy()
	}
	reissued, err := s.operator.ReissueOperatorTeamAdmin(r.Context(), a, r.PathValue("team"), request.RevokePrevious)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusCreated, controllerwire.OperatorTeamAdminReissued{
		OperatorTeamCreated: operatorTeamAdmin(reissued.Issued),
		ExpiresAt:           *reissued.Issued.Capability.ExpiresAt, Revoked: reissued.Revoked,
	})
	return nil
}

func operatorTeamAdmin(issued capability.Issued) controllerwire.OperatorTeamCreated {
	return controllerwire.OperatorTeamCreated{
		Team: issued.Capability.Team, AccountID: issued.Capability.Subject.ID,
		Capability: issued.Raw, Prefix: issued.Capability.Prefix, Grants: issued.Capability.Grants,
	}
}
