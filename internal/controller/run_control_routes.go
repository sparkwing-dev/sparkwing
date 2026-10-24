package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// runControlWriter rechecks authority, masks public text and commits control events and replay together.
type runControlWriter interface {
	Team() store.Team
	CreateAuthorizedDebugPause(context.Context, capability.Authorized, string, string, time.Time, string) error
	ResolveAuthorizedApproval(context.Context, capability.Authorized, store.ApprovalDecision) (store.ApprovalResult, error)
}

// debugPauseResumer closes an authorized job pause in its committing transaction.
type debugPauseResumer interface {
	Team() store.Team
	ResumeAuthorizedDebugPause(context.Context, capability.Authorized) error
}

func (s *Server) runControlWriter(r *http.Request, a capability.Authorized) (runControlWriter, error) {
	return tenantPort[runControlWriter](r.Context(), s, store.Team(a.Target.Team))
}

func (s *Server) runControlEndpoints() []controllerwire.Endpoint {
	engine := s.engineController
	return []controllerwire.Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/resume", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleResumeDebugPause},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/debug-pauses", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleCreateDebugPause},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/approvals/{job}", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: engine.resolveRunSelector, Handle: s.handleResolveApproval},
	}
}

func (s *Server) handleResumeDebugPause(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := decodeEngineRequest(r, &struct{}{}); err != nil {
		return err
	}
	writer, err := tenantPort[debugPauseResumer](r.Context(), s, store.Team(a.Target.Team))
	if err != nil {
		return err
	}
	if err := writer.ResumeAuthorizedDebugPause(r.Context(), a); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

var (
	_ debugPauseResumer = (*store.Tenant)(nil)
	_ runControlWriter  = (*store.Tenant)(nil)
)

func (s *Server) handleCreateDebugPause(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct {
		JobID     string    `json:"job_id"`
		Reason    string    `json:"reason"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.JobID) == "" || strings.TrimSpace(body.Reason) == "" {
		return invalidEngineBody("job_id and reason are required")
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	writer, err := s.runControlWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.CreateAuthorizedDebugPause(r.Context(), a, body.JobID, body.Reason, body.ExpiresAt, key); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.ApprovalDecision
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Resolution != store.ApprovalResolutionApproved && body.Resolution != store.ApprovalResolutionDenied {
		return invalidEngineBody("resolution must be approved or denied")
	}
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	body.IdempotencyKey = key
	writer, err := s.runControlWriter(r, a)
	if err != nil {
		return err
	}
	out, err := writer.ResolveAuthorizedApproval(r.Context(), a, store.ApprovalDecision(body))
	if err != nil {
		return engineLookupError(err)
	}
	if out.Approval.RunID != a.Target.RunID || out.Approval.NodeID != a.Target.NodeID {
		return routepolicy.MissingEndpointPolicy()
	}
	writeJSON(w, http.StatusOK, controllerwire.ApprovalResult{Approval: approvalView(out.Approval), Replayed: out.Replayed})
	return nil
}

func controlIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(values[0], "\r\n\x00") {
		return "", invalidEngineBody("one nonempty Idempotency-Key is required")
	}
	return values[0], nil
}
