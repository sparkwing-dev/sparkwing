package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type whoamiJSON struct {
	Principal   string                 `json:"principal"`
	Kind        capability.SubjectKind `json:"kind"`
	Scopes      []capability.Grant     `json:"scopes"`
	TokenPrefix string                 `json:"token_prefix,omitempty"`
	Team        string                 `json:"team,omitempty"`
}

func (s *Server) handleIdentity(w http.ResponseWriter, _ *http.Request, a capability.Authorized) error {
	c := a.Principal.Capability
	grants := append([]capability.Grant{}, c.Grants...)
	writeJSON(w, http.StatusOK, whoamiJSON{
		Principal: c.Subject.ID, Kind: c.Subject.Kind, Scopes: grants,
		TokenPrefix: principalCapabilityPrefix(a.Principal), Team: c.Team,
	})
	return nil
}
