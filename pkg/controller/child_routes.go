package controller

import (
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func (s *Server) childEndpoints(e *EngineController) []Endpoint {
	return []Endpoint{
		{Method: "POST", Pattern: "/api/v1/runs/{run}/children", Authentication: capability.AuthCapability, Decode: e.childIntakeTarget, Handle: s.handleIntake(e)},
		{Method: "GET", Pattern: "/api/v1/runs/{run}/children/{child}", Authentication: capability.AuthCapability, Decode: e.childTarget, Handle: e.handleChild},
	}
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
