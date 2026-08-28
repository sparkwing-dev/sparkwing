package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) handleRequestNodeBounce(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	b, err := s.store.RequestNodeBounce(r.Context(), runID, nodeID, auditPrincipal(r))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrNodeNotRunning), errors.Is(err, store.ErrRunNotLive):
		writeError(w, http.StatusConflict, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, b)
	}
}

func (s *Server) handlePendingNodeBounce(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.PendingNodeBounce(r.Context(), r.PathValue("id"), r.PathValue("nodeID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if b == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) handleConsumeNodeBounce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seq     int64  `json:"seq"`
		Outcome string `json:"outcome"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if body.Outcome == "" {
		body.Outcome = store.BounceBounced
	}
	err := s.store.ConsumeNodeBounce(r.Context(),
		r.PathValue("id"), r.PathValue("nodeID"), body.Seq, body.Outcome)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
