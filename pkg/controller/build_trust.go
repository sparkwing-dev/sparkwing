package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type buildTrustRequest struct {
	TrustLocalBuilds bool `json:"trust_local_builds"`
}

func (s *Server) handleGetBuildTrust(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	trust, err := s.store.TrustLocalBuilds(r.Context(), t.Team())
	if err != nil {
		s.writeInternalError(w, r, "read build trust", err)
		return
	}
	writeJSON(w, http.StatusOK, buildTrustRequest{TrustLocalBuilds: trust})
}

func (s *Server) handlePutBuildTrust(w http.ResponseWriter, r *http.Request) {
	_, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req buildTrustRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SetTrustLocalBuilds(r.Context(), t.Team(), req.TrustLocalBuilds); err != nil {
		s.writeInternalError(w, r, "set build trust", err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
