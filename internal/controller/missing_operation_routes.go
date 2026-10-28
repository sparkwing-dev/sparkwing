package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) missingOperationEndpoints() []controllerwire.Endpoint {
	e := s.engineController
	endpoints := []controllerwire.Endpoint{
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/debug-pause", Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleJobDebugPause},
		{Method: "DELETE", Pattern: "/api/v1/runners/{runner}", Decode: authorityTarget, Handle: s.handleDeleteRunner},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/attempts", Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleJobAttempts},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/steps", Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleJobSteps},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/activity", Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleJobActivity},
		{Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/claim", Decode: e.claimTarget, ResolveTarget: e.resolveRunSelector, Handle: s.claimHandler(e)},
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
	nodes := make(map[string]runstate.NodeRow, len(view.Snapshot.Nodes))
	attempts := make([]controllerwire.AttemptDetail, 0)
	found := false
	for _, row := range view.Snapshot.Nodes {
		nodes[string(row.Ref.NodeID)] = row
		if row.Ref.NodeID == runspec.NodeID(a.Target.NodeID) {
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
	if _, err := tenant.ReadNode(r.Context(), runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}); err != nil {
		return engineLookupError(err)
	}
	all, err := tenant.ListNodeSteps(r.Context(), a.Target.RunID)
	if err != nil {
		return engineLookupError(err)
	}
	steps := make([]controllerwire.StepObservation, 0)
	for _, step := range all {
		if step.NodeID == a.Target.NodeID {
			steps = append(steps, controllerwire.StepObservation{
				RunID: step.RunID, NodeID: step.NodeID, StepID: step.StepID, Attempt: step.Attempt, Status: step.Status, Error: step.Error,
				StartedAt: step.StartedAt, FinishedAt: step.FinishedAt, Annotations: publicAnnotations(step.Annotations), Summary: step.Summary,
			})
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
	if err := tenant.WriteActivity(r.Context(), runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}, engineActor(a), body.Detail); err != nil {
		return engineLookupError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleDeleteRunner(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	name := r.PathValue("runner")
	if !store.ValidRunnerName(name) {
		return invalidEngineBody("invalid runner name")
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	revoked, err := tenant.DeleteRunner(r.Context(), engineActor(a), name)
	if err != nil {
		return engineLookupError(err)
	}
	if revoked != nil {
		noteAuditTarget(r.Context(), "machine_id", revoked.MachineID)
		noteAuditTarget(r.Context(), "token_prefixes", strings.Join(revoked.TokenPrefixes, ","))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runner": name, "deleted": revoked != nil})
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
	if _, err := tenant.ReadNode(r.Context(), runspec.NodeRef{RunID: runspec.RunID(a.Target.RunID), NodeID: runspec.NodeID(a.Target.NodeID)}); err != nil {
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
