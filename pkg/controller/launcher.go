package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type launchClaimReq struct {
	HolderID     string `json:"holder_id"`
	LeaseSecs    int    `json:"lease_secs"`
	DeadlineSecs int    `json:"deadline_secs"`
}

// safety: a 204 when no node is ready keeps an idle launcher's poll to one read.
func (s *Server) handleLaunchClaim(w http.ResponseWriter, r *http.Request) {
	var req launchClaimReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	claim, err := s.store.ClaimLaunch(r.Context(), claimIdentity(r), store.LaunchClaimRequest{
		HolderID: req.HolderID,
		Lease:    time.Duration(req.LeaseSecs) * time.Second,
		Deadline: time.Duration(req.DeadlineSecs) * time.Second,
	}, time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "launch claim", err)
	case claim == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, claim)
	}
}
