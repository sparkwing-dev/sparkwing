package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the plan route's path names no node, so it binds the planning node,
// which is the only node a plan claim can hold.
func planClaimBinding(r *http.Request) (string, string) {
	return r.PathValue("id"), store.PlanNodeID
}

func (s *Server) handleAcceptPlan(w http.ResponseWriter, r *http.Request, commit store.ClaimResultCommit) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	replayed, err := s.store.AcceptPlan(r.Context(), commit, body, time.Now())
	s.writeClaimResult(w, r, "accepted", replayed, err)
}

func (s *Server) handleReportAttempt(w http.ResponseWriter, r *http.Request, commit store.ClaimResultCommit) {
	var report store.AttemptReport
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	replayed, err := s.store.ReportAttempt(r.Context(), commit, report, time.Now())
	s.writeClaimResult(w, r, "recorded", replayed, err)
}

func (s *Server) writeClaimResult(w http.ResponseWriter, r *http.Request, status string, replayed bool, err error) {
	switch {
	case err == nil && replayed:
		writeJSON(w, http.StatusOK, map[string]string{"status": "replayed"})
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	case errors.Is(err, store.ErrPlanInvalid), errors.Is(err, store.ErrAttemptInvalid):
		writeError(w, http.StatusUnprocessableEntity, err)
	case errors.Is(err, store.ErrClaimResultConflict), errors.Is(err, store.ErrClaimNotLive):
		writeError(w, http.StatusConflict, err)
	default:
		writeClaimRefusal(w, r, s, err)
	}
}
