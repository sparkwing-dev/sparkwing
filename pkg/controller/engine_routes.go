package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithEngineController supplies the intake/sealing transaction adapter before
// Handler is constructed. The default adapter uses the server's store.
func (s *Server) WithEngineController(engine *EngineController) *Server {
	s.engineController = engine
	return s
}

func (s *Server) engineEndpoints() []Endpoint {
	engine := s.engineController
	if engine == nil {
		engine = &EngineController{Store: s.store}
	}
	return []Endpoint{
		{
			Method: "POST", Pattern: "/api/v1/runs/{run}/jobs/{job}/expansion", Authentication: capability.AuthCapability,
			Decode: engine.expansionTarget,
			Handle: func(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
				body, err := decodeExpansion(r)
				if err != nil {
					return err
				}
				accepted, err := engine.AcceptExpansion(r.Context(), authorized, body)
				if err != nil {
					return err
				}
				writeJSON(w, http.StatusOK, accepted)
				return nil
			},
		},
	}
}

func (s *Server) registerEngineRoutes(mux *http.ServeMux) {
	s.registerPolicyEndpoints(mux, s.engineEndpoints())
}

func (s *EngineController) expansionTarget(r *http.Request) (capability.RequestEvidence, error) {
	ref := store.NodeRef{RunID: r.PathValue("run"), NodeID: r.PathValue("job")}
	evidence := capability.RequestEvidence{Target: capability.Target{RunID: ref.RunID, NodeID: ref.NodeID}}
	var team store.Team
	var err error
	if s.ResolveRunTeam != nil {
		team, err = s.ResolveRunTeam(r.Context(), ref.RunID)
	} else if s.Store != nil {
		team, err = s.Store.AsOperator().RunTeam(r.Context(), ref.RunID)
	} else {
		return evidence, missingEndpointPolicy()
	}
	if err != nil {
		return evidence, engineLookupError(err)
	}
	evidence.Target.Team = string(team)
	tenant, err := s.runTransactions(r.Context(), string(team))
	if err != nil {
		return evidence, err
	}
	node, err := tenant.ReadNode(r.Context(), ref)
	if err != nil {
		return evidence, engineLookupError(err)
	}
	if node.Ref != ref || node.Generation <= 0 {
		return evidence, missingEndpointPolicy()
	}
	evidence.Target.UnitSourceNodeID, evidence.Target.UnitSourceGeneration = node.Ref.NodeID, node.Generation
	return evidence, nil
}

func engineLookupError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &apierr.Error{Code: apierr.NotFound, Message: "resource not found"}
	}
	return err
}

func decodeExpansion(r *http.Request) (controllerwire.ExpansionSubmission, error) {
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return controllerwire.ExpansionSubmission{}, invalidEngineBody("content-type application/json required")
	}
	defer r.Body.Close()
	var wire struct {
		SourceGeneration int64           `json:"source_generation"`
		Body             json.RawMessage `json:"body"`
		Failure          json.RawMessage `json:"failure"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxPlanSnapshotBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return controllerwire.ExpansionSubmission{}, invalidEngineBody("invalid expansion request")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return controllerwire.ExpansionSubmission{}, invalidEngineBody("expansion requires one JSON object")
	}
	body := controllerwire.ExpansionSubmission{SourceGeneration: wire.SourceGeneration, Body: wire.Body}
	if len(wire.Failure) > 0 && !bytes.Equal(bytes.TrimSpace(wire.Failure), []byte("null")) {
		failure, err := apierr.DecodeInner(wire.Failure)
		if err != nil {
			return controllerwire.ExpansionSubmission{}, invalidEngineBody("invalid expansion failure")
		}
		body.Failure = failure
	}
	return body, nil
}
