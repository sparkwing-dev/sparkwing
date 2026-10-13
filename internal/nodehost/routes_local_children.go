package nodehost

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrChildCycle is a child run that would start a pipeline already among the
// parent run's ancestors.
var ErrChildCycle = errors.New("the pipeline is already among this run's ancestors")

// ChildParent is the run and node a child run is started from, with the run's
// admission lease the child attaches to; LeaseToken is empty without one.
type ChildParent struct {
	RunID, NodeID, LeaseToken string
}

// LocalChildren starts and reads the child runs of a run that no controller
// holds, through this machine's trigger queue.
type LocalChildren interface {
	StartChild(ctx context.Context, parent ChildParent, req ChildRequest) (runID string, err error)
	GetRun(ctx context.Context, runID string) (*store.Run, error)
}

// perf: a local store read is cheap, so a child's end reaches its parent within a quarter second.
const localAwaitPoll = 250 * time.Millisecond

func (s *Server) serveLocalStartChild(w http.ResponseWriter, r *http.Request, g *grant, req ChildRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()
	id, err := g.Children.StartChild(ctx, ChildParent{RunID: g.RunID, NodeID: g.NodeID, LeaseToken: g.LeaseToken}, req)
	switch {
	case errors.Is(err, ErrChildCycle):
		s.writeError(w, http.StatusConflict, "cycle", "pipeline "+quote(req.Pipeline)+" is already among this run's ancestors")
	case err != nil:
		s.localOutputFailed(w, g, "children", err)
	default:
		g.started.Store(id, true)
		s.writeJSON(w, http.StatusCreated, map[string]string{"run_id": id})
	}
}

// safety: as with a claim, a node awaits only a child it started.
func (s *Server) serveLocalAwaitChild(w http.ResponseWriter, r *http.Request, g *grant, child, node string, wait time.Duration) {
	if _, ok := g.started.Load(child); !ok {
		s.logger.Warn("audit", "event", "child_unknown", "run", g.RunID, "node", g.NodeID, "child", child)
		s.writeError(w, http.StatusNotFound, "unknown_child", "node "+quote(g.NodeID)+" did not start run "+quote(child))
		return
	}
	// safety: a node blocked on its child prints nothing, so the await holds its no-progress timeout, as in-process.
	if g.Pause != nil {
		defer g.Pause()()
	}
	ctx := r.Context()
	deadline := time.Now().Add(wait)
	status, errMsg := "pending", ""
	for {
		run, err := g.Children.GetRun(ctx, child)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// safety: a queued trigger has no run row yet, so the child reads as pending.
		case err != nil:
			s.localOutputFailed(w, g, "await", err)
			return
		default:
			status, errMsg = run.Status, run.Error
		}
		if terminalRun(status) || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(min(localAwaitPoll, time.Until(deadline))):
		}
	}
	reply := map[string]any{"run_id": child, "status": status}
	if errMsg != "" {
		reply["error"] = errMsg
	}
	if status != "success" || node == "" {
		s.writeJSON(w, http.StatusOK, reply)
		return
	}
	if g.Local == nil {
		s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "this node has no runs store to read the child's output from")
		return
	}
	if _, err := g.Local.GetNode(ctx, child, node); errors.Is(err, store.ErrNotFound) {
		s.writeError(w, http.StatusNotFound, "unknown_node", "run "+quote(child)+" has no node "+quote(node))
		return
	} else if err != nil {
		s.localOutputFailed(w, g, "await", err)
		return
	}
	s.writeLocalOutput(ctx, w, g, "await", child, node, reply)
}
