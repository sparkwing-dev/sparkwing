package controller

import (
	"context"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) childEndpoints(e *EngineController) []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/children", Authentication: capability.AuthCapability, Decode: e.childIntakeTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleIntake(e)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/children/{child}", Authentication: capability.AuthCapability, Decode: e.childTarget, ResolveTarget: e.resolveRunSelector, Handle: e.handleChild},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/jobs/{job}/output", Authentication: capability.AuthCapability, Decode: runSelectorTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleJobOutput},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", Authentication: capability.AuthCapability, Decode: e.childTarget, ResolveTarget: e.resolveRunSelector, Handle: s.handleChildOutput},
	}
}

func (s *Server) handleChildOutput(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	return s.handleOutput(w, r, a, true)
}

func (s *Server) handleJobOutput(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	return s.handleOutput(w, r, a, false)
}

func (s *Server) handleOutput(w http.ResponseWriter, r *http.Request, a capability.Authorized, child bool) error {
	if s.operator == nil || s.dataController == nil {
		return missingEndpointPolicy()
	}
	objects, ok := s.dataController.Objects.(interface {
		ReadOutput(context.Context, blob.Blob) ([]byte, error)
	})
	if !ok {
		return missingEndpointPolicy()
	}
	tenant, err := s.tenantForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	read := tenant.ReadJobOutput
	if child {
		read = tenant.ReadChildOutput
	}
	object, err := read(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	output, err := objects.ReadOutput(r.Context(), object)
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.JobOutput{Output: output})
	return nil
}

func (s *EngineController) childIntakeTarget(r *http.Request) (capability.RequestEvidence, error) {
	evidence, _ := runSelectorTarget(r)
	var body controllerwire.CreateRunRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return evidence, err
	}
	lineage := body.Request.Lineage
	if lineage.ParentRunID != evidence.Target.RunID || lineage.ParentJobID == "" || lineage.RetryOf != "" {
		return evidence, invalidEngineBody("child intake must name its path parent and parent job")
	}
	evidence.Target.BodyRunID = lineage.ParentRunID
	evidence.Target.NodeID = lineage.ParentJobID
	return evidence, nil
}

func (s *EngineController) childTarget(r *http.Request) (capability.RequestEvidence, error) {
	evidence, err := runSelectorTarget(r)
	evidence.Target.ChildRunID = r.PathValue("child")
	return evidence, err
}

func (s *EngineController) handleChild(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	a.Target.RunID = a.Target.ChildRunID
	a.Target.NodeID = ""
	return s.handleRunDetail(w, r, a)
}
