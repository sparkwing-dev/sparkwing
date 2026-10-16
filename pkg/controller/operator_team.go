package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleOperatorCreateTeam(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var request controllerwire.OperatorTeamCreate
	if err := decodeEngineRequest(r, &request); err != nil {
		return err
	}
	if s.store == nil {
		return missingEndpointPolicy()
	}
	issued, err := s.store.CreateOperatorTeam(r.Context(), a, request.Slug, request.DisplayName)
	if errors.Is(err, store.ErrSlugTaken) {
		return &apierr.Error{Code: apierr.Conflict, Message: "team already exists or its slug is retired"}
	}
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusCreated, controllerwire.OperatorTeamCreated{
		Team: issued.Capability.Team, AccountID: issued.Capability.Subject.ID,
		Capability: issued.Raw, Prefix: issued.Capability.Prefix, Grants: issued.Capability.Grants,
	})
	return nil
}
