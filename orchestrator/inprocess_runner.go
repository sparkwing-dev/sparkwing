package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// InProcessRunner executes nodes in the orchestrator's own goroutine.
// Owns per-node execution: cache, SkipIf, lock, run + hooks, terminal
// state. Stateless beyond Backends.
type InProcessRunner struct {
	backends Backends
}

// NewInProcessRunner builds a runner over Backends; lifecycle is
// caller-owned.
func NewInProcessRunner(backends Backends) *InProcessRunner {
	return &InProcessRunner{backends: backends}
}

var _ runner.Runner = (*InProcessRunner)(nil)

// runJobBody executes the node's materialized Work as a step DAG.
// Returns the typed output of the *WorkStep the Job's Work returned
// (nil for untyped Jobs).
func runJobBody(ctx context.Context, node *sparkwing.Node) (any, error) {
	w := node.Work()
	if w == nil {
		return nil, fmt.Errorf("sparkwing: node %q has no Work; non-approval nodes must be registered via plan.Job", node.ID())
	}
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		return nil, err
	}
	if rs := node.ResultStep(); rs != nil {
		return rs.Output(), nil
	}
	return nil, nil
}

// stateMetricsSink adapts StateBackend to nodemetrics.Sink. Errors
// are dropped: losing a sample is better than failing the node.
type stateMetricsSink struct {
	backend StateBackend
	runID   string
	nodeID  string
}

func (s stateMetricsSink) Push(ctx context.Context, sample nodemetrics.Sample) error {
	return s.backend.AddNodeMetricSample(ctx, s.runID, s.nodeID, store.MetricSample{
		TS:            sample.TS,
		CPUMillicores: sample.CPUMillicores,
		MemoryBytes:   sample.MemoryBytes,
	})
}

// RunNode executes one node to a terminal outcome. ctx carries the
// ref resolver and propagates cancellation to job + hooks + SkipIf.
func (r *InProcessRunner) RunNode(ctx context.Context, req runner.Request) runner.Result {
	node := req.Node
	if node == nil {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("InProcessRunner: Request.Node is nil for %s/%s", req.RunID, req.NodeID),
		}
	}

	// .Cache() delegates the full acquire/run/release cycle.
	if result, handled := r.runNodeWithCache(ctx, req); handled {
		return result
	}

	if reason, skip := evalSkipPredicates(ctx, node); skip {
		r.markSkipped(ctx, req.RunID, node.ID(), reason)
		return runner.Result{Outcome: sparkwing.Skipped}
	}

	output, err := r.executeNode(ctx, req.RunID, node, req.Delegate)
	if err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

// executeNode runs the job with modifiers + hooks and persists state.
func (r *InProcessRunner) executeNode(ctx context.Context, runID string, node *sparkwing.Node, delegate sparkwing.Logger) (any, error) {
	nlog, err := r.backends.Logs.OpenNodeLog(runID, node.ID(), delegate)
	if err != nil {
		return nil, err
	}
	// Redact secrets before persist + delegate.
	nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
	// Persist sparkwing.Annotate() messages onto the node row.
	nlog = wrapNodeLogWithAnnotations(nlog, r.backends.State, runID, node.ID())
	// Persist sparkwing.Summary() markdown onto the node / step row.
	nlog = wrapNodeLogWithSummary(nlog, r.backends.State, runID, node.ID())
	// Persist step_start / step_end / step_skipped to node_steps rows.
	nlog = wrapNodeLogWithStepState(nlog, r.backends.State, runID, node.ID())
	defer nlog.Close()

	if err := r.backends.State.StartNode(ctx, runID, node.ID()); err != nil {
		return nil, err
	}
	_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_started", nil)

	nodeStartTS := time.Now()
	nlog.Emit(sparkwing.LogRecord{
		TS:    nodeStartTS,
		Level: "info",
		Event: "node_start",
	})
	emitNodeEnd := func(outcome sparkwing.Outcome, errMsg string) {
		attrs := map[string]any{
			"outcome":     string(outcome),
			"duration_ms": time.Since(nodeStartTS).Milliseconds(),
		}
		if errMsg != "" {
			attrs["error"] = errMsg
		}
		nlog.Emit(sparkwing.LogRecord{
			TS:    time.Now(),
			Level: "info",
			Event: "node_end",
			Attrs: attrs,
		})
	}

	// Per-node attribution is approximate when nodes share a process.
	samplerCtx, stopSampler := context.WithCancel(ctx)
	go nodemetrics.Run(samplerCtx, 2*time.Second, stateMetricsSink{
		backend: r.backends.State,
		runID:   runID,
		nodeID:  node.ID(),
	})
	defer stopSampler()

	hbCtx, stopHB := context.WithCancel(ctx)
	go runNodeHeartbeatLoop(hbCtx, 5*time.Second, r.backends.State, runID, node.ID())
	defer stopHB()

	nodeCtx := sparkwing.WithLogger(ctx, nlog)
	nodeCtx = sparkwing.WithNode(nodeCtx, node.ID())

	// Snapshot before BeforeRun so replay re-runs hooks fresh.
	// Best-effort: snapshot failures don't fail the node.
	if err := r.writeDispatchSnapshot(nodeCtx, runID, node); err != nil {
		sparkwing.Debug(nodeCtx, "dispatch snapshot: %v", err)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "dispatch_snapshot_failed", []byte(err.Error()))
	}

	for i, hook := range node.BeforeRunHooks() {
		sparkwing.Debug(nodeCtx, "hook: BeforeRun[%d] firing", i)
		if err := callBeforeRun(nodeCtx, hook); err != nil {
			wrapped := fmt.Errorf("BeforeRun hook %d: %w", i, err)
			nlog.Log("error", wrapped.Error())
			emitNodeEnd(sparkwing.Failed, wrapped.Error())
			_ = r.backends.State.FinishNode(ctx, runID, node.ID(), string(sparkwing.Failed), wrapped.Error(), nil)
			_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(wrapped.Error()))
			return nil, wrapped
		}
	}

	// When RetryConfig.Auto is set, dispatch owns retry; the in-runner
	// step loop must not also retry or budgets multiply.
	retryCfg := node.RetryConfig()
	attempts := retryCfg.Attempts
	backoff := retryCfg.Backoff
	if retryCfg.Auto {
		attempts = 0
		backoff = 0
	}
	timeout := node.TimeoutDuration()

	var output any
	var lastErr error
	var lastTimeout bool
	total := attempts + 1
	for attempt := range total {
		if attempt > 0 {
			wait := scaledBackoff(backoff, attempt)
			msg := fmt.Sprintf("retry attempt %d/%d", attempt+1, total)
			if wait > 0 {
				msg = fmt.Sprintf("retry attempt %d/%d after %s", attempt+1, total, wait)
			}
			nlog.Emit(sparkwing.LogRecord{
				TS:    time.Now(),
				Level: "info",
				Event: "retry",
				Msg:   msg,
				Attrs: map[string]any{"attempt": attempt + 1, "total": total},
			})
			if wait > 0 {
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					lastErr = ctx.Err()
					goto done
				}
			}
			_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "attempt_retry", fmt.Appendf(nil, "attempt %d/%d", attempt+1, total))
		}

		attemptCtx := nodeCtx
		var cancel context.CancelFunc
		if timeout > 0 {
			attemptCtx, cancel = context.WithTimeout(nodeCtx, timeout)
		}
		out, aerr := runJobBody(attemptCtx, node)
		if cancel != nil {
			cancel()
		}
		if aerr == nil {
			output = out
			lastErr = nil
			break
		}
		lastErr = aerr
		timedOut := false
		if timeout > 0 && errors.Is(aerr, context.DeadlineExceeded) && nodeCtx.Err() == nil {
			// Attempt ctx fired but parent is live: Timeout modifier,
			// not operator cancel.
			lastErr = fmt.Errorf("timeout exceeded (%s): %w", timeout, aerr)
			timedOut = true
		}
		lastTimeout = timedOut
		// IMP-NOTE: we used to also emit a `level=error` log line
		// here re-stating lastErr.Error(). That duplicated the
		// structured error already on step_end.attrs.error, doubling
		// every failure record an agent had to dedupe. The pretty
		// renderer now surfaces the error message directly under the
		// merged step_end/node_end line by reading attrs.error from
		// step_end -- single source of truth, no duplicates.
	}

done:
	for i, hook := range node.AfterRunHooks() {
		sparkwing.Debug(nodeCtx, "hook: AfterRun[%d] firing (err=%v)", i, lastErr)
		callAfterRun(nodeCtx, hook, lastErr, i, nlog)
	}

	// A sticky logs-append auth failure must fail the node even if
	// the user job body returned success, since the run's observable
	// logs are gone. Auth wins over a transient timeout/error since
	// fixing the user code can't unblock it.
	if fatal := nodeLogFatal(nlog); fatal != nil {
		wrapped := fmt.Errorf("logs append blocked; failing node: %w", fatal)
		emitNodeEnd(sparkwing.Failed, wrapped.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), wrapped.Error(), nil, store.FailureLogsAuth, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(wrapped.Error()))
		return nil, wrapped
	}

	if lastErr != nil {
		reason := store.FailureUnknown
		if lastTimeout {
			reason = store.FailureTimeout
		}
		emitNodeEnd(sparkwing.Failed, lastErr.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), lastErr.Error(), nil, reason, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(lastErr.Error()))
		return nil, lastErr
	}

	// Surface soft drops on the run summary so 5xx-driven log loss
	// stops being a silent observability hole. Best-effort event;
	// renderers can also aggregate from `logs_drop` events later.
	if count, reason := nodeLogDrops(nlog); count > 0 {
		payload, _ := json.Marshal(map[string]any{"count": count, "reason": reason})
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "logs_drop", payload)
	}

	var outBytes []byte
	if output != nil {
		if b, merr := json.Marshal(output); merr == nil {
			outBytes = b
		}
	}
	emitNodeEnd(sparkwing.Success, "")
	_ = r.backends.State.FinishNode(ctx, runID, node.ID(), string(sparkwing.Success), "", outBytes)
	_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_succeeded", nil)

	// Memoization runs in the concurrency primitive's release path.
	return output, nil
}

// nodeLogFatal returns the sticky auth error from a NodeLog that
// implements the optional Fataler interface. NodeLog impls without
// auth-aware retry (localLogs, fakes) return nil here, matching the
// no-fatal-state default.
func nodeLogFatal(nlog NodeLog) error {
	if f, ok := nlog.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

// nodeLogDrops returns the (count, first-reason) tuple from a
// NodeLog that implements the optional Dropper interface.
func nodeLogDrops(nlog NodeLog) (int, string) {
	if d, ok := nlog.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}

func (r *InProcessRunner) markSkipped(ctx context.Context, runID, nodeID, reason string) {
	_ = r.backends.State.FinishNode(ctx, runID, nodeID, string(sparkwing.Skipped), reason, nil)
	_ = r.backends.State.AppendEvent(ctx, runID, nodeID, "node_skipped", []byte(reason))
}

func (r *InProcessRunner) markFailed(ctx context.Context, runID, nodeID string, reason error) {
	_ = r.backends.State.FinishNode(ctx, runID, nodeID, string(sparkwing.Failed), reason.Error(), nil)
	_ = r.backends.State.AppendEvent(ctx, runID, nodeID, "node_failed", []byte(reason.Error()))
}
