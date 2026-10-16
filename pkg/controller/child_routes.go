package controller

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *Server) childEndpoints(e *EngineController) []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/children", Authentication: capability.AuthCapability, Decode: e.childIntakeTarget, Handle: s.handleIntake(e)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/children/{child}", Authentication: capability.AuthCapability, Decode: e.childTarget, Handle: e.handleChild},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/children/{child}/jobs/{job}/output", Authentication: capability.AuthCapability, Decode: e.childTarget, Handle: s.handleChildOutput},
	}
}

func (s *Server) handleChildOutput(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	if s.store == nil || s.dataController == nil {
		return missingEndpointPolicy()
	}
	objects, ok := s.dataController.Objects.(interface {
		ReadOutput(context.Context, blob.Blob) ([]byte, error)
	})
	if !ok {
		return missingEndpointPolicy()
	}
	tenant, err := s.store.ForTeam(r.Context(), store.Team(a.Target.Team))
	if err != nil {
		return engineLookupError(err)
	}
	object, err := tenant.ReadChildOutput(r.Context(), a)
	if err != nil {
		return dataRouteError(err)
	}
	output, err := objects.ReadOutput(r.Context(), object)
	if err != nil {
		return dataRouteError(err)
	}
	writeJSON(w, http.StatusOK, struct {
		Output json.RawMessage `json:"output"`
	}{output})
	return nil
}

func (s *EngineController) childIntakeTarget(r *http.Request) (capability.RequestEvidence, error) {
	evidence, err := s.runTarget(r)
	if err != nil {
		return evidence, err
	}
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
	evidence, err := s.runTarget(r)
	evidence.Target.ChildRunID = r.PathValue("child")
	return evidence, err
}

func (s *EngineController) handleChild(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	a.Target.RunID = a.Target.ChildRunID
	a.Target.NodeID = ""
	return s.handleRunDetail(w, r, a)
}
