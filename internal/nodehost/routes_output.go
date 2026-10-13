package nodehost

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: the run is always the grant's, so a node never names one.
func (s *Server) serveNodeOutput(w http.ResponseWriter, r *http.Request, g *grant) {
	node := r.PathValue("node")
	if !validNodeID(node) {
		s.writeError(w, http.StatusBadRequest, "bad_request", "the node id is malformed")
		return
	}
	if g.Claim == nil && g.Local != nil {
		s.serveLocalNodeOutput(w, r, g, node)
		return
	}
	claim, ctx, done, ok := s.forward(w, r, g)
	if !ok {
		return
	}
	defer done()
	var row struct {
		Status  string `json:"status"`
		Outcome string `json:"outcome"`
	}
	status, _, err := claim.call(ctx, http.MethodGet, runPath(g.RunID, "nodes", node), nil, &row)
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "output", status, err)
		return
	case status == http.StatusNotFound:
		s.writeError(w, http.StatusNotFound, "unknown_node", "the run has no node "+quote(node))
		return
	case status != http.StatusOK:
		s.upstreamFailed(w, g, "output", status, nil)
		return
	case row.Status != "done":
		s.writeError(w, http.StatusConflict, "not_ready", "node "+quote(node)+" has not finished")
		return
	case row.Outcome != "success":
		s.writeError(w, http.StatusNotFound, "no_output", "node "+quote(node)+" finished without succeeding")
		return
	}
	var grant store.OutputReadGrant
	status, code, err := claim.call(ctx, http.MethodGet, runPath(g.RunID, "nodes", node, "output"), nil, &grant)
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "output", status, err)
		return
	case status == http.StatusForbidden && code == "output_not_ancestor":
		s.logger.Warn("audit", "event", "output_not_dependency", "run", g.RunID, "node", g.NodeID, "target", node)
		s.writeError(w, http.StatusForbidden, "not_dependency",
			"node "+quote(g.NodeID)+" reads only the outputs of nodes it depends on")
		return
	case status != http.StatusOK:
		s.upstreamFailed(w, g, "output", status, nil)
		return
	}
	s.writeOutput(ctx, w, g, "output", grant, map[string]any{})
}

// safety: the controller resolves the run within the claim's team and only
// for a reference the node's plan declares.
func (s *Server) servePipelineLatest(w http.ResponseWriter, r *http.Request, g *grant) {
	pipeline, node := r.PathValue("name"), r.URL.Query().Get("node")
	maxAge, ageOK := int64(0), true
	if raw := r.URL.Query().Get("max_age_ms"); raw != "" {
		var err error
		maxAge, err = strconv.ParseInt(raw, 10, 64)
		ageOK = err == nil && maxAge >= 0
	}
	if !validName(pipeline) || !validNodeID(node) || !ageOK {
		s.writeError(w, http.StatusBadRequest, "bad_request", "the pipeline, node or max_age_ms is malformed")
		return
	}
	if g.Claim == nil && g.Local != nil {
		s.serveLocalPipelineLatest(w, r, g, pipeline, node, time.Duration(maxAge)*time.Millisecond)
		return
	}
	claim, ctx, done, ok := s.forward(w, r, g)
	if !ok {
		return
	}
	defer done()
	var in store.ClaimInput
	status, code, err := claim.call(ctx, http.MethodPost, runPath(g.RunID, "nodes", g.NodeID, "claim", "input"),
		store.ClaimInputRequest{Kind: store.ClaimInputLastRun, Pipeline: pipeline, Node: node, MaxAgeMS: maxAge}, &in)
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "latest", status, err)
		return
	case status == http.StatusNotFound:
		s.writeError(w, http.StatusNotFound, "no_run", "no successful run of "+quote(pipeline)+" matches")
		return
	case status == http.StatusForbidden && code == "input_undeclared":
		s.logger.Warn("audit", "event", "reference_undeclared", "run", g.RunID, "node", g.NodeID, "pipeline", pipeline)
		s.writeError(w, http.StatusForbidden, "reference_undeclared",
			"node "+quote(g.NodeID)+" does not declare a reference to "+quote(pipeline+"/"+node))
		return
	case status != http.StatusOK:
		s.upstreamFailed(w, g, "latest", status, nil)
		return
	case in.Output.URL == "":
		s.writeError(w, http.StatusNotFound, "no_output", "node "+quote(node)+" of the newest run recorded no output")
		return
	}
	s.writeOutput(ctx, w, g, "latest", in.Output, map[string]any{"run_id": in.RunID})
}
