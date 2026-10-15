package controller

import (
	"errors"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"net/http"
)

func (s *Server) missingOperationEndpoints() []Endpoint {
	e := s.engineController
	if e == nil {
		e = &EngineController{Store: s.store}
	}
	endpoints := []Endpoint{
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/debug-pause", Decode: e.runTarget, Handle: s.handleJobDebugPause},
		{Method: "DELETE", Pattern: "/api/v1/runners/{runner}", Decode: authorityTarget, Handle: s.handleDeleteRunner},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/attempts", Decode: e.runTarget, Handle: s.handleJobAttempts},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps", Decode: e.runTarget, Handle: s.handleJobSteps},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/activity", Decode: e.runTarget, Handle: s.handleJobActivity},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/claim", Decode: e.claimTarget, Handle: s.claimHandler(e)},
	}
	endpoints = append(endpoints, s.logSubmissionEndpoints(e)...)
	endpoints = append(endpoints, s.poolEndpoints()...)
	endpoints = append(endpoints, s.childEndpoints(e)...)
	for i := range endpoints {
		endpoints[i].Authentication = capability.AuthCapability
	}
	return endpoints
}

func (s *Server) handleJobAttempts(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	view, err := tenant.ReadRunView(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	nodes := make(map[string]store.NodeRow, len(view.Snapshot.Nodes))
	attempts := make([]controllerwire.AttemptDetail, 0)
	found := false
	for _, row := range view.Snapshot.Nodes {
		nodes[row.Ref.NodeID] = row
		if row.Ref.NodeID == a.Target.NodeID {
			found = true
		}
	}
	if !found {
		return engineLookupError(store.ErrNotFound)
	}
	projected, err := statusAttempts(a.Target, nodes, view.Attempts)
	if err != nil {
		return err
	}
	for _, attempt := range projected {
		if attempt.JobID == a.Target.NodeID {
			attempts = append(attempts, attempt)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": attempts})
	return nil
}

func (s *Server) handleJobSteps(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	if _, err := tenant.ReadNode(r.Context(), store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}); err != nil {
		return engineLookupError(err)
	}
	all, err := tenant.ListNodeSteps(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	steps := make([]*store.NodeStep, 0)
	for _, step := range all {
		if step.NodeID == a.Target.NodeID {
			steps = append(steps, step)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
	return nil
}

func (s *Server) handleJobActivity(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body struct {
		Detail string `json:"detail"`
	}
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	if err := tenant.WriteActivity(r.Context(), store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}, engineActor(a), body.Detail); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	name := r.PathValue("runner")
	if !validRunnerName(name) {
		return invalidEngineBody("invalid runner name")
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	deleted, err := tenant.DeleteRunner(r.Context(), engineActor(a), name)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runner": name, "deleted": deleted})
	return nil
}

func (s *Server) handleJobDebugPause(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	run, err := tenant.ReadRun(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	if _, err := tenant.ReadNode(r.Context(), store.NodeRef{RunID: a.Target.RunID, NodeID: a.Target.NodeID}); err != nil {
		return engineLookupError(err)
	}
	pause, err := tenant.GetActiveDebugPause(r.Context(), a.Target.RunID, a.Target.NodeID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return engineLookupError(err)
	}
	result := controllerwire.DebugPauseResult{RunID: a.Target.RunID, Number: run.Number, JobID: a.Target.NodeID}
	if pause != nil {
		result.Pause = &controllerwire.DebugPause{Reason: pause.Reason, PausedAt: pause.PausedAt, ExpiresAt: &pause.ExpiresAt}
	}
	if err := result.Validate(a.Target.RunID, a.Target.NodeID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
