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
	if g.Claim == nil {
		s.writeError(w, http.StatusBadGateway, "upstream_unavailable", "this node has no controller to forward to")
		return
	}
	// safety: only a child this claim started is reachable; the controller
	// checks the same, and the host refuses before asking it or admitting the wait.
	if !g.Claim.startedChild(child) {
		s.logger.Warn("audit", "event", "child_unknown", "run", g.RunID, "node", g.NodeID, "child", child)
		s.writeError(w, http.StatusNotFound, "unknown_child", "node "+quote(g.NodeID)+" did not start run "+quote(child))
		return
	}
	claim, ctx, done, ok := s.admit(w, r, g, g.Claim.awaitSlots, "child-run awaits")
	if !ok {
		return
	}
	defer done()
	// safety: a node blocked on its child prints nothing, so the await holds its no-progress timeout, as in-process.
	if g.Pause != nil {
		defer g.Pause()()
	}
	run, status, err := claim.await(ctx, g, child, wait)
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

// safety: one poller per child serves every await of it, so the
// controller's load grows with the children a node waits on, not its awaits.
type childWatch struct {
	waiters int
	polled  bool
	run     childRun
	status  int
	err     error
	changed chan struct{}
	stop    context.CancelFunc
}

func terminalRun(status string) bool {
	return status == "success" || status == "failed" || status == "cancelled"
}

// safety: the poller runs under the grant's context, so revoking the grant or
// closing the server stops it.
func (c *Claim) await(ctx context.Context, g *grant, child string, wait time.Duration) (childRun, int, error) {
	c.mu.Lock()
	cw := c.watches[child]
	if cw == nil {
		pctx, stop := context.WithCancel(g.ctx)
		cw = &childWatch{changed: make(chan struct{}), stop: stop}
		c.watches[child] = cw
		//nolint:contextcheck // the poller outlives any one await, so it runs under the grant's context.
		go c.poll(pctx, g.RunID, child, cw)
	}
	cw.waiters++
	c.mu.Unlock()
	defer c.leave(child, cw)

	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	expired := false
	for {
		c.mu.Lock()
		run, status, err, polled, changed := cw.run, cw.status, cw.err, cw.polled, cw.changed
		c.mu.Unlock()
		if polled && (err != nil || status != http.StatusOK || terminalRun(run.Status) || expired) {
			return run, status, err
		}
		select {
		case <-ctx.Done():
			return run, 0, ctx.Err()
		case <-deadline.C:
			expired = true
		case <-changed:
		}
	}
}

func (c *Claim) leave(child string, cw *childWatch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cw.waiters--
	if cw.waiters == 0 {
		cw.stop()
		if c.watches[child] == cw {
			delete(c.watches, child)
		}
	}
}

func (c *Claim) poll(ctx context.Context, runID, child string, cw *childWatch) {
	defer cw.stop()
	tick := time.NewTicker(awaitPoll)
	defer tick.Stop()
	for {
		var run childRun
		status, _, err := c.call(ctx, http.MethodGet, runPath(runID, "children", child), nil, &run)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		c.mu.Lock()
		cw.run, cw.status, cw.err, cw.polled = run, status, err, true
		close(cw.changed)
		cw.changed = make(chan struct{})
		end := err != nil || status != http.StatusOK || terminalRun(run.Status)
		if end && c.watches[child] == cw {
			delete(c.watches, child)
		}
		c.mu.Unlock()
		if end {
			return
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
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
