package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

type buildTrustRequest struct {
	TrustLocalBuilds bool `json:"trust_local_builds"`
}

func (s *Server) handleGetBuildTrust(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	t, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	trust, err := t.TrustLocalBuilds(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, buildTrustRequest{TrustLocalBuilds: trust})
	return nil
}

func (s *Server) handlePutBuildTrust(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var req buildTrustRequest
	if err := decodeEngineRequest(r, &req); err != nil {
		return err
	}
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.SetAuthorizedBuildTrust(r.Context(), a, req.TrustLocalBuilds); err != nil {
		return accountMutationError(err)
	}
	writeJSON(w, http.StatusOK, req)
	return nil
}
