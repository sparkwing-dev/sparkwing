package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
	case errors.Is(err, store.ErrAttemptInvalid):
		writeError(w, http.StatusUnprocessableEntity, err)
	case errors.Is(err, store.ErrClaimResultConflict), errors.Is(err, store.ErrClaimNotLive):
		writeError(w, http.StatusConflict, err)
	default:
		writeClaimRefusal(w, r, s, err)
	}
}
