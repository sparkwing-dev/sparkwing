package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// LocalOutputs answers the output routes from this machine's own runs store,
// for a run that no controller holds and so has no [Claim] to forward with.
type LocalOutputs interface {
	GetNode(ctx context.Context, runID, nodeID string) (*store.Node, error)
	GetNodeOutput(ctx context.Context, runID, nodeID string) ([]byte, error)
	GetLatestRun(ctx context.Context, pipeline string, statuses []string, maxAge time.Duration) (*store.Run, error)
}

// safety: as the controller does for a claim, a node reads only the outputs
// of its transitive dependencies, the only nodes its plan could wait for.
func (s *Server) serveLocalNodeOutput(w http.ResponseWriter, r *http.Request, g *grant, node string) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	ancestor, err := localAncestor(ctx, g.Local, g.RunID, g.NodeID, node)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "unknown_node", "the run has no node "+quote(node))
		return
	case err != nil:
		s.localOutputFailed(w, g, "output", err)
		return
	case !ancestor:
		s.logger.Warn("audit", "event", "output_not_dependency", "run", g.RunID, "node", g.NodeID, "target", node)
		s.writeError(w, http.StatusForbidden, "not_dependency",
			"node "+quote(g.NodeID)+" reads only the outputs of nodes it depends on")
		return
	}
	row, err := g.Local.GetNode(ctx, g.RunID, node)
	switch {
	case err != nil:
		s.localOutputFailed(w, g, "output", err)
		return
	case row.Status != "done":
		s.writeError(w, http.StatusConflict, "not_ready", "node "+quote(node)+" has not finished")
		return
	case row.Outcome != "success":
		s.writeError(w, http.StatusNotFound, "no_output", "node "+quote(node)+" finished without succeeding")
		return
	}
	s.writeLocalOutput(ctx, w, g, "output", g.RunID, node, map[string]any{})
}

func (s *Server) serveLocalPipelineLatest(w http.ResponseWriter, r *http.Request, g *grant, pipeline, node string, maxAge time.Duration) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	run, err := g.Local.GetLatestRun(ctx, pipeline, []string{"success"}, maxAge)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "no_run", "no successful run of "+quote(pipeline)+" matches")
		return
	case err != nil:
		s.localOutputFailed(w, g, "latest", err)
		return
	}
	s.writeLocalOutput(ctx, w, g, "latest", run.ID, node, map[string]any{"run_id": run.ID})
}

func (s *Server) writeLocalOutput(ctx context.Context, w http.ResponseWriter, g *grant, route, runID, node string, reply map[string]any) {
	data, err := g.Local.GetNodeOutput(ctx, runID, node)
	switch {
	case errors.Is(err, store.ErrNotFound) && route == "latest":
		s.writeError(w, http.StatusNotFound, "no_output", "node "+quote(node)+" of the newest run recorded no output")
		return
	case err != nil:
		s.localOutputFailed(w, g, route, err)
		return
	case len(data) == 0:
		data = []byte("null")
	}
	reply["output"] = json.RawMessage(data)
	s.writeJSON(w, http.StatusOK, reply)
}

// safety: a store error can quote paths, so only its type is logged and the
// node gets a fixed message.
func (s *Server) localOutputFailed(w http.ResponseWriter, g *grant, route string, err error) {
	s.logger.Warn("node route local read failed", "run", g.RunID, "node", g.NodeID, "route", route, "err_type", fmt.Sprintf("%T", err))
	s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "the runs store could not be read; retry after a delay")
}

// safety: walks the node rows rather than the plan, so a generated node, whose deps are only on its row, is covered.
func localAncestor(ctx context.Context, st LocalOutputs, runID, node, target string) (bool, error) {
	if _, err := st.GetNode(ctx, runID, target); err != nil {
		return false, err
	}
	seen := map[string]bool{node: true}
	queue := []string{node}
	for len(queue) > 0 {
		row, err := st.GetNode(ctx, runID, queue[0])
		queue = queue[1:]
		if err != nil {
			return false, err
		}
		for _, dep := range row.Deps {
			if dep == target {
				return true, nil
			}
			if !seen[dep] {
				seen[dep] = true
				queue = append(queue, dep)
			}
		}
	}
	return false, nil
}
