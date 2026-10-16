package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunControlWriter rechecks authority, masks public text and commits control events and replay together.
type RunControlWriter interface {
	Team() store.Team
	CreateAuthorizedDebugPause(context.Context, capability.Authorized, string, string, time.Time, string) error
	ResolveAuthorizedApproval(context.Context, capability.Authorized, controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error)
}

// DebugPauseResumer closes an authorized job pause in its committing transaction.
type DebugPauseResumer interface {
	Team() store.Team
	ResumeAuthorizedDebugPause(context.Context, capability.Authorized) error
}

// WithRunControlWriter supplies tenant run-control transactions before Handler is constructed.
func (s *Server) WithRunControlWriter(resolve func(context.Context, store.Team) (RunControlWriter, error)) *Server {
	s.resolveRunControlWriter = resolve
	return s
}

func (s *Server) runControlWriter(r *http.Request, a capability.Authorized) (RunControlWriter, error) {
	if a.Target.Team == "" {
		return nil, missingEndpointPolicy()
	}
	var writer RunControlWriter
	var err error
	if s.resolveRunControlWriter != nil {
		writer, err = s.resolveRunControlWriter(r.Context(), store.Team(a.Target.Team))
	} else if s.store != nil {
		var tenant *store.Tenant
		tenant, err = s.reportingTenant(r, a)
		writer, _ = any(tenant).(RunControlWriter)
	}
	if err != nil {
		return nil, engineLookupError(err)
	}
	if writer == nil || writer.Team() != store.Team(a.Target.Team) {
		return nil, missingEndpointPolicy()
	}
	return writer, nil
}

func (s *Server) runControlEndpoints() []Endpoint {
	engine := s.engineController
	if engine == nil {
		engine = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/resume", Authentication: capability.AuthCapability, Decode: engine.runTarget, Handle: s.handleResumeDebugPause},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/debug-pauses", Authentication: capability.AuthCapability, Decode: engine.runTarget, Handle: s.handleCreateDebugPause},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/approvals/{job}", Authentication: capability.AuthCapability, Decode: engine.runTarget, Handle: s.handleResolveApproval},
	}
}

func (s *Server) handleResumeDebugPause(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if err := decodeEngineRequest(r, &struct{}{}); err != nil {
		return err
	}
	var writer DebugPauseResumer
	if s.resolveRunControlWriter != nil {
		control, err := s.runControlWriter(r, a)
		if err != nil {
			return err
		}
		writer, _ = control.(DebugPauseResumer)
	} else {
		tenant, err := s.reportingTenant(r, a)
		if err != nil {
			return err
		}
		writer = tenant
	}
	if writer == nil || string(writer.Team()) != a.Target.Team {
		return missingEndpointPolicy()
	}
	if err := writer.ResumeAuthorizedDebugPause(r.Context(), a); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

var (
	_ DebugPauseResumer = (*store.Tenant)(nil)
	_ RunControlWriter  = (*store.Tenant)(nil)
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
	out, err := writer.ResolveAuthorizedApproval(r.Context(), a, body)
	if err != nil {
		return engineLookupError(err)
	}
	if out.Approval.RunID != a.Target.RunID || out.Approval.JobID != a.Target.NodeID {
		return missingEndpointPolicy()
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func controlIdempotencyKey(r *http.Request) (string, error) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.ContainsAny(values[0], "\r\n\x00") {
		return "", invalidEngineBody("one nonempty Idempotency-Key is required")
	}
	return values[0], nil
}
