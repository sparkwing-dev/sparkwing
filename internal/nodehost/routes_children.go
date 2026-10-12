package nodehost

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	defaultAwait = 30 * time.Second
	maxAwait     = 60 * time.Second
	// perf: a child run takes seconds at least, so a poll a second keeps the
	// controller's load per waiting node small.
	awaitPoll = time.Second
	// safety: a child's args are a pipeline's inputs, not data, so a small
	// body bounds what a node can make the host hold.
	maxChildBody = 64 << 10
)

type childRequest struct {
	Pipeline string            `json:"pipeline"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

// safety: the controller reads the parent run, node and attempt from the
// claim, so the node names none of them.
func (s *Server) serveStartChild(w http.ResponseWriter, r *http.Request, g *grant) {
	claim, ctx, done, ok := s.forward(w, r, g)
	if !ok {
		return
	}
	defer done()
	var req childRequest
	if !decodeBody(r, maxChildBody, &req) || !validName(req.Pipeline) || len(req.Args) > 256 {
		s.writeError(w, http.StatusBadRequest, "bad_request", "the body must name a pipeline, in at most 64 KiB")
		return
	}
	if !acquire(ctx, claim.childSlot) {
		s.upstreamFailed(w, g, "children", 0, ctx.Err())
		return
	}
	defer func() { <-claim.childSlot }()
	var out struct {
		RunID string `json:"run_id"`
	}
	status, code, err := claim.call(ctx, http.MethodPost, runPath(g.RunID, "children"), struct {
		Ordinal int64 `json:"ordinal"`
		childRequest
	}{claim.nextChild, req}, &out)
	// safety: an ordinal is spent only once the controller answered, so a
	// retry after a lost reply returns the child the first call started.
	if err == nil && status < http.StatusInternalServerError {
		claim.nextChild++
	}
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "children", status, err)
	case status == http.StatusAccepted && validName(out.RunID):
		claim.recordChild(out.RunID)
		s.writeJSON(w, http.StatusCreated, map[string]string{"run_id": out.RunID})
	case status == http.StatusConflict && strings.HasPrefix(code, "cycle"):
		s.writeError(w, http.StatusConflict, "cycle", "pipeline "+quote(req.Pipeline)+" is already among this run's ancestors")
	case status == http.StatusUnprocessableEntity:
		s.writeError(w, http.StatusUnprocessableEntity, "child_repo", "a child run builds its parent's repository and commit only")
	case status == http.StatusBadRequest:
		s.writeError(w, http.StatusBadRequest, "bad_request", "the controller refused the child run's request")
	default:
		s.upstreamFailed(w, g, "children", status, nil)
	}
}

func (s *Server) serveAwaitChild(w http.ResponseWriter, r *http.Request, g *grant) {
	child, node := r.PathValue("run"), r.URL.Query().Get("node")
	wait, waitOK := waitParam(r.URL.Query().Get("wait_ms"))
	if !validName(child) || (node != "" && !validNodeID(node)) || !waitOK {
		s.writeError(w, http.StatusBadRequest, "bad_request", "the run id, node or wait_ms is malformed")
		return
	}
	claim, ctx, done, ok := s.forward(w, r, g)
	if !ok {
		return
	}
	defer done()
	// safety: only a child this claim started is reachable; the controller
	// checks the same, and the host refuses before asking it.
	if !claim.startedChild(child) {
		s.logger.Warn("audit", "event", "child_unknown", "run", g.RunID, "node", g.NodeID, "child", child)
		s.writeError(w, http.StatusNotFound, "unknown_child", "node "+quote(g.NodeID)+" did not start run "+quote(child))
		return
	}
	run, status, err := awaitChild(ctx, claim, g.RunID, child, wait)
	switch {
	case err != nil:
		s.upstreamFailed(w, g, "await", status, err)
		return
	case status == http.StatusNotFound:
		s.writeError(w, http.StatusNotFound, "unknown_child", "node "+quote(g.NodeID)+" did not start run "+quote(child))
		return
	case status != http.StatusOK:
		s.upstreamFailed(w, g, "await", status, nil)
		return
	}
	reply := map[string]any{"run_id": child, "status": run.Status}
	if run.Error != "" {
		reply["error"] = run.Error
	}
	if run.Status == "success" && node != "" {
		var grant store.OutputReadGrant
		status, _, err := claim.call(ctx, http.MethodGet, runPath(g.RunID, "children", child, "nodes", node, "output"), nil, &grant)
		switch {
		case err != nil:
			s.upstreamFailed(w, g, "await", status, err)
			return
		case status == http.StatusNotFound:
			s.writeError(w, http.StatusNotFound, "unknown_node", "run "+quote(child)+" has no node "+quote(node))
			return
		case status != http.StatusOK:
			s.upstreamFailed(w, g, "await", status, nil)
			return
		}
		s.writeOutput(ctx, w, g, "await", grant, reply)
		return
	}
	s.writeJSON(w, http.StatusOK, reply)
}

type childRun struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

func awaitChild(ctx context.Context, claim *Claim, runID, child string, wait time.Duration) (childRun, int, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	poll := time.NewTicker(awaitPoll)
	defer poll.Stop()
	for {
		var run childRun
		status, _, err := claim.call(ctx, http.MethodGet, runPath(runID, "children", child), nil, &run)
		if err != nil || status != http.StatusOK {
			return run, status, err
		}
		switch run.Status {
		case "success", "failed", "cancelled":
			return run, status, nil
		}
		select {
		case <-ctx.Done():
			return run, 0, ctx.Err()
		case <-deadline.C:
			return run, status, nil
		case <-poll.C:
		}
	}
}

func waitParam(raw string) (time.Duration, bool) {
	if raw == "" {
		return defaultAwait, true
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms < 0 || time.Duration(ms)*time.Millisecond > maxAwait {
		return 0, false
	}
	return time.Duration(ms) * time.Millisecond, true
}
