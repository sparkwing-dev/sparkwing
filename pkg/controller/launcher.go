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

// safety: until run-node and plan --json report through claim tokens and pods
// fetch claim-bound source, a controller-dispatched run cannot finish, so the
// route refuses to opt a repository in; the store keeps the setting for tests.
const controllerDispatchComplete = false

type repoDispatchReq struct {
	Dispatch store.RepoDispatch `json:"dispatch"`
}

func (s *Server) handleSetRepoDispatch(w http.ResponseWriter, r *http.Request) {
	t, ok := s.namedTenant(w, r, r.PathValue("team"))
	if !ok {
		return
	}
	var req repoDispatchReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Dispatch == store.RepoDispatchController && !controllerDispatchComplete {
		writeError(w, http.StatusConflict, errors.New(
			"controller dispatch cannot run a pipeline yet: pods do not fetch source or report through claim tokens"))
		return
	}
	owner, name := r.PathValue("owner"), r.PathValue("name")
	err := t.SetRepoDispatch(r.Context(), owner, name, req.Dispatch, time.Now())
	switch {
	case errors.Is(err, store.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, err)
	case err != nil:
		s.writeInternalError(w, r, "repository dispatch", err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{
			"team": string(t.Team()), "repo": store.RepoKey(owner, name), "dispatch": string(req.Dispatch),
		})
	}
}
