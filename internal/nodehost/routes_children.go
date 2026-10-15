package nodehost

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
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

// ChildRequest starts a child pipeline and selects its eventual output job.
type ChildRequest struct {
	Pipeline string            `json:"pipeline"`
	Job      string            `json:"job,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
	Repo     string            `json:"repo,omitempty"`
	Branch   string            `json:"branch,omitempty"`
}

func (g *grant) startChild(ctx context.Context, request ChildRequest) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	defer cancel()
	if !validName(request.Pipeline) || len(request.Args) > 256 {
		return "", executorError(apierr.BadRequest, "invalid child pipeline or arguments")
	}
	request.Job = ""
	if g.Claim == nil && g.Children != nil {
		return g.localStartChild(ctx, request)
	}
	if g.Claim == nil {
		return "", serviceUnavailable()
	}
	if !acquire(ctx, g.Claim.childSlot) {
		return "", ctx.Err()
	}
	defer func() { <-g.Claim.childSlot }()
	var out struct {
		RunID string `json:"run_id"`
	}
	status, code, err := g.Claim.call(ctx, http.MethodPost, runPath(g.RunID, "children"), struct {
		Ordinal int64 `json:"ordinal"`
		ChildRequest
	}{g.Claim.nextChild, request}, &out)
	if err == nil && status < http.StatusInternalServerError {
		g.Claim.nextChild++
	}
	switch {
	case err != nil:
		return "", serviceUnavailable()
	case status == http.StatusAccepted && validName(out.RunID):
		g.Claim.recordChild(out.RunID)
		return out.RunID, nil
	case status == http.StatusConflict && strings.HasPrefix(code, "cycle"):
		return "", routeFailure(apierr.Conflict, apierr.ReasonChildCycle, "pipeline is already an ancestor")
	case status == http.StatusUnprocessableEntity:
		return "", executorError(apierr.PlanInvalid, "child must build its parent repository and commit")
	case status == http.StatusBadRequest:
		return "", executorError(apierr.BadRequest, "child request was refused")
	default:
		return "", serviceUnavailable()
	}
}

func (g *grant) awaitChild(ctx context.Context, child, job string, wait time.Duration) (ProcessChildResult, error) {
	if !validName(child) || (job != "" && !validNodeID(job)) {
		return ProcessChildResult{}, executorError(apierr.BadRequest, "invalid child run or job")
	}
	if g.Claim == nil && g.Children != nil {
		return g.localAwaitChild(ctx, child, job, wait)
	}
	if g.Claim == nil {
		return ProcessChildResult{}, serviceUnavailable()
	}
	if !g.Claim.startedChild(child) {
		return ProcessChildResult{}, executorError(apierr.NotFound, "process did not start this child")
	}
	done, err := claimSlot(ctx, g.Claim.awaitSlots)
	if err != nil {
		return ProcessChildResult{}, err
	}
	defer done()
	if g.Pause != nil {
		defer g.Pause()()
	}
	run, status, err := g.Claim.await(ctx, g, child, wait)
	if status == http.StatusNotFound {
		return ProcessChildResult{}, executorError(apierr.NotFound, "process did not start this child")
	}
	if err != nil || status != http.StatusOK {
		return ProcessChildResult{}, serviceUnavailable()
	}
	result := ProcessChildResult{RunID: child, Status: run.Status, Error: run.Error}
	if run.Status != "success" || job == "" {
		return result, nil
	}
	var output store.OutputReadGrant
	status, _, err = g.Claim.call(ctx, http.MethodGet, runPath(g.RunID, "children", child, "nodes", job, "output"), nil, &output)
	if status == http.StatusNotFound {
		return ProcessChildResult{}, executorError(apierr.UnknownJob, "child has no such job")
	}
	if err != nil || status != http.StatusOK {
		return ProcessChildResult{}, serviceUnavailable()
	}
	result.Output, err = g.Claim.output(ctx, output)
	return result, err
}

type childRun struct {
	Status string        `json:"status"`
	Error  *apierr.Error `json:"error,omitempty"`
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
	wait, err := serviceDuration(raw)
	if err != nil || wait > maxAwait {
		return 0, false
	}
	return wait, true
}
