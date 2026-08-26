package controller

import (
	"errors"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Node-bounce endpoints: an operator records the intent to restart one
// running node's process, and the runner supervising that process
// polls for it and reports what it did.
//
// Both halves go through the controller so that the verb behaves the
// same against a hosted controller and against the loopback one a
// local run mounts for its own node processes. The runner has no
// database handle of its own -- reaching the state through a
// controller is the whole point of the loopback -- so its poll and its
// consume are HTTP calls too.

// handleRequestNodeBounce records the intent. The store owns the
// guards; the mapping here is what makes them honest over the wire: an
// unknown run or node is 404, a node with no process to kill is 409,
// and the message carries the status the operator needs to see.
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

// handlePendingNodeBounce answers a supervising runner's poll. No
// pending request is the common answer and is 204, not 404: nothing is
// missing, there is simply nothing to do.
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

// handleConsumeNodeBounce closes a request with the outcome the runner
// produced: it killed the process and re-ran the node, or the node
// reached its terminal row first and the request missed.
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
