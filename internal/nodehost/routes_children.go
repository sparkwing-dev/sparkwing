package nodehost

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const (
	defaultAwait = 30 * time.Second
	maxAwait     = 60 * time.Second
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
	ctx, cancel := context.WithTimeout(ctx, localServiceTimeout)
	defer cancel()
	if !validName(request.Pipeline) || len(request.Args) > 256 {
		return "", executorError(apierr.BadRequest, "invalid child pipeline or arguments")
	}
	request.Job = ""
	if g.Children == nil {
		return "", serviceUnavailable()
	}
	return g.localStartChild(ctx, request)
}

func (g *grant) awaitChild(ctx context.Context, child, job string, wait time.Duration) (ProcessChildResult, error) {
	if !validName(child) || (job != "" && !validNodeID(job)) {
		return ProcessChildResult{}, executorError(apierr.BadRequest, "invalid child run or job")
	}
	if g.Children == nil {
		return ProcessChildResult{}, serviceUnavailable()
	}
	return g.localAwaitChild(ctx, child, job, wait)
}

func terminalRun(status string) bool {
	return status == "success" || status == "failed" || status == "cancelled"
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
