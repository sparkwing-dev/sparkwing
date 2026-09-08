package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// PriorityEnv carries `sparkwing run --sw-priority` to the pipeline program:
// an integer, or `front` / `back` for the queue-relative forms.
const PriorityEnv = "SPARKWING_PRIORITY"

const priorityQueueQueryTimeout = 5 * time.Second

// priorityRequest is one run's answer to --sw-priority, ready to override
// whatever the plan declared.
type priorityRequest struct {
	value int
	// source names where value came from, for the run's recorded flags:
	// "flag" for a literal integer, "front" or "back" for a resolved one.
	source string
}

// resolveRunPriority reads the operator's --sw-priority request and settles it
// against the daemon's queue. front and back are measured once, here, so run
// admission and every later node admission share one number.
func resolveRunPriority(ctx context.Context, raw string, admission *LocalAdmission) (priorityRequest, bool, error) {
	mode := strings.TrimSpace(raw)
	if mode == "" {
		return priorityRequest{}, false, nil
	}
	if n, err := strconv.Atoi(mode); err == nil {
		return priorityRequest{value: n, source: "flag"}, true, nil
	}
	if mode != wingwire.PriorityFront && mode != wingwire.PriorityBack {
		return priorityRequest{}, false, fmt.Errorf(
			"%s=%q: expected an integer, %q, or %q", PriorityEnv, raw, wingwire.PriorityFront, wingwire.PriorityBack)
	}
	waiters, err := currentQueueWaiters(ctx, admission)
	if err != nil {
		return priorityRequest{}, false, fmt.Errorf("--sw-priority %s: %w", mode, err)
	}
	return priorityRequest{value: wingwire.ResolveRelativePriority(waiters, mode), source: mode}, true, nil
}

// safety: a daemon that is absent is an empty queue, but one that exists and
// will not answer fails the run: this run has to be admitted by that same
// daemon, so guessing a position here only moves the failure later.
func currentQueueWaiters(ctx context.Context, admission *LocalAdmission) ([]wingwire.Waiter, error) {
	opts := wingdclient.Options{Version: sparkwingModuleVersion()}
	if admission != nil {
		opts.Home = admission.Home
		if admission.Version != "" {
			opts.Version = admission.Version
		}
	}
	qctx, cancel := context.WithTimeout(ctx, priorityQueueQueryTimeout)
	defer cancel()
	qs, err := wingdclient.Query(qctx, opts)
	if err != nil {
		if errors.Is(err, wingdclient.ErrNoDaemon) {
			return nil, nil
		}
		return nil, err
	}
	return qs.Waiters, nil
}
