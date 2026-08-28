package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type requestApprovalReq struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

func (s *Server) handleRequestApproval(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body requestApprovalReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if runID == "" || nodeID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run id and node id required"))
		return
	}
	onTimeout := body.OnTimeout
	switch onTimeout {
	case "", store.ApprovalOnTimeoutFail, store.ApprovalOnTimeoutDeny, store.ApprovalOnTimeoutApprove:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid on_timeout: %q", onTimeout))
		return
	}
	if onTimeout == "" {
		onTimeout = store.ApprovalOnTimeoutFail
	}
	if err := s.store.CreateApproval(r.Context(), store.Approval{
		RunID:       runID,
		NodeID:      nodeID,
		RequestedAt: time.Now(),
		Message:     body.Message,
		TimeoutMS:   body.TimeoutMS,
		OnTimeout:   onTimeout,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"message":    body.Message,
		"timeout_ms": body.TimeoutMS,
	})
	_, _ = s.store.AppendEvent(r.Context(), runID, nodeID, "approval_requested", payload)
	w.WriteHeader(http.StatusCreated)
}

type resolveApprovalReq struct {
	Resolution string `json:"resolution"`
	Comment    string `json:"comment,omitempty"`
	// safety: authenticated requests derive the approver from the principal.
	Approver string `json:"approver,omitempty"`
}

func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	var body resolveApprovalReq
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch body.Resolution {
	case store.ApprovalResolutionApproved,
		store.ApprovalResolutionDenied,
		store.ApprovalResolutionTimedOut:
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid resolution: %q", body.Resolution))
		return
	}
	approver := body.Approver
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil {
		approver = p.Name
	}
	if approver == "" {
		approver = "unknown"
	}

	got, err := s.store.ResolveApproval(r.Context(), runID, nodeID,
		body.Resolution, approver, body.Comment)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if errors.Is(err, store.ErrLockHeld) {
			writeError(w, http.StatusConflict, errors.New("approval already resolved"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"resolution": got.Resolution,
		"approver":   got.Approver,
		"comment":    got.Comment,
	})
	_, _ = s.store.AppendEvent(r.Context(), runID, nodeID, "approval_resolved", payload)
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	a, err := s.store.GetApproval(r.Context(), runID, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleListApprovalsForRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	rows, err := s.store.ListApprovalsForRun(r.Context(), runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []*store.Approval{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": rows})
}

func (s *Server) handleListPendingApprovals(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListPendingApprovals(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if rows == nil {
		rows = []*store.Approval{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": rows})
}
