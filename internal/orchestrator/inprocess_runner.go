package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// InProcessRunner executes nodes in the orchestrator's own goroutine.
// Owns per-node execution: cache, SkipIf, lock, run + hooks, terminal
// state. Stateless beyond Backends.
type InProcessRunner struct {
	backends Backends
	labels   []string
}

// NewInProcessRunner builds a runner over Backends; lifecycle is
// caller-owned. The runner advertises a "local" label by default so
// jobs declaring WhenRunner("local") dispatch through it.
func NewInProcessRunner(backends Backends) *InProcessRunner {
	return &InProcessRunner{backends: backends, labels: []string{"local"}}
}

// AdvertisedLabels implements runner.LabelAdvertiser. The default set
// is ["local"]; callers wiring an in-process runner with additional
// capabilities (a USB-attached device, a specific OS) can override
// via SetLabels before the orchestrator dispatch loop starts.
func (r *InProcessRunner) AdvertisedLabels() []string {
	out := make([]string, len(r.labels))
	copy(out, r.labels)
	return out
}

// SetLabels replaces the advertised label set. Intended for callers
// that wire an InProcessRunner outside NewInProcessRunner's defaults
// (host-side runners with hardware affinity, test fixtures pinning a
// custom label set). The orchestrator's WhenRunner evaluation reads
// the labels via AdvertisedLabels.
func (r *InProcessRunner) SetLabels(labels []string) {
	if len(labels) == 0 {
		r.labels = nil
		return
	}
	r.labels = make([]string, 0, len(labels))
	for _, l := range labels {
		if l == "" {
			continue
		}
		r.labels = append(r.labels, l)
	}
}

var (
	_ runner.Runner          = (*InProcessRunner)(nil)
	_ runner.LabelAdvertiser = (*InProcessRunner)(nil)
)

// runJobBody executes the node's materialized Work as a step DAG.
// Returns the typed output of the *WorkStep the Job's Work returned
// (nil for untyped Jobs).
func runJobBody(ctx context.Context, node *sparkwing.JobNode) (any, error) {
	w := node.Work()
	if w == nil {
		return nil, fmt.Errorf("sparkwing: node %q has no Work; non-approval nodes must be registered via plan.Job", node.ID())
	}
	if _, err := sparkwing.RunWork(ctx, w); err != nil {
		return nil, wrapNodeError(node.ID(), err)
	}
	if rs := node.ResultStep(); rs != nil {
		return rs.Output(), nil
	}
	return nil, nil
}

// wrapNodeError prefixes err with the node ID so dispatch-level
// failure messages identify the failing node by default. Authors who
// already include the node ID (or any "<id>:" prefix) keep their
// richer message intact -- the check is a literal prefix match, which
// favors false-negatives (double-wraps for unusual prefixes) over
// false-positives.
func wrapNodeError(nodeID string, err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), nodeID+":") {
		return err
	}
	return fmt.Errorf("%s: %w", nodeID, err)
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

	if result, handled := r.runNodeWithCache(ctx, req); handled {
		return result
	}

	if reason, skip := evalSkipPredicates(ctx, node); skip {
		r.markSkipped(ctx, req.RunID, node.ID(), reason)
		return runner.Result{Outcome: sparkwing.Skipped}
	}

	output, err := r.executeNodeWithAdmission(ctx, req)
	if err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

func (r *InProcessRunner) executeNodeWithAdmission(ctx context.Context, req runner.Request) (any, error) {
	la, _, hostAdmitted := localAdmissionFromContext(ctx)
	if la == nil || hostAdmitted {
		return r.executeNode(ctx, req.RunID, req.Node, req.Delegate)
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = req.Node.ID()
	}
	if req.ReleaseWorkerSlot != nil {
		req.ReleaseWorkerSlot()
	}
	lease, err := la.admitNode(ctx, r.backends, req.Pipeline, req.RunID, nodeID, req.Node)
	if req.ReacquireWorkerSlot != nil && !req.ReacquireWorkerSlot() {
		if lease != nil {
			lease.release()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	defer lease.release()
	_, childToken, _ := localAdmissionFromContext(ctx)
	nodeCtx := withLocalAdmission(ctx, la, lease.token, childToken, lease.hostAdmitted)
	return r.executeNode(nodeCtx, req.RunID, req.Node, req.Delegate)
}

// executeNode runs the job with modifiers + hooks and persists state.
func (r *InProcessRunner) executeNode(ctx context.Context, runID string, node *sparkwing.JobNode, delegate sparkwing.Logger) (any, error) {
	nlog, err := r.backends.Logs.OpenNodeLog(runID, node.ID(), delegate)
	if err != nil {
		return nil, err
	}
	nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
	nlog = wrapNodeLogWithAnnotations(nlog, r.backends.State, runID, node.ID())
	nlog = wrapNodeLogWithSummary(nlog, r.backends.State, runID, node.ID())
	nlog = wrapNodeLogWithStepState(nlog, r.backends.State, runID, node.ID())
	defer func() { _ = nlog.Close() }()

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

	samplerCtx, stopSampler := context.WithCancel(ctx)
	go nodemetrics.Run(samplerCtx, 2*time.Second, stateMetricsSink{
		backend: r.backends.State,
		runID:   runID,
		nodeID:  node.ID(),
	})
	defer stopSampler()

	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}
	hbCtx, stopHB := context.WithCancel(ctx)
	go runNodeHeartbeatLoop(hbCtx, 5*time.Second, r.backends.State, runID, node.ID(), wedgeBudget)
	defer stopHB()

	nodeCtx := sparkwingruntime.WithLogger(ctx, nlog)
	nodeCtx = sparkwingruntime.WithNode(nodeCtx, node.ID())
	nodeCtx = sparkwing.WithResourceReporter(nodeCtx, func(s sparkwing.ResourceSample) {
		nodemetrics.AddReportedChildCPU(s.CPUTime)
		_ = r.backends.State.AddNodeMetricSample(ctx, runID, node.ID(), store.MetricSample{
			TS:            time.Now(),
			CPUMillicores: s.CPUMillicores,
			MemoryBytes:   s.MemoryBytes,
		})
	})

	if err := r.writeDispatchSnapshot(nodeCtx, runID, node); err != nil {
		sparkwing.Debug(nodeCtx, "dispatch snapshot: %v", err)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "dispatch_snapshot_failed", []byte(err.Error()))
	}

	if staged, serr := r.stageArtifacts(nodeCtx, runID, node); serr != nil {
		wrapped := fmt.Errorf("stage consumed artifacts: %w", serr)
		nlog.Log("error", wrapped.Error())
		emitNodeEnd(sparkwing.Failed, wrapped.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), wrapped.Error(), nil, store.FailureUnknown, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(wrapped.Error()))
		return nil, wrapped
	} else if staged > 0 {
		payload, _ := json.Marshal(map[string]any{"files": staged})
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "artifacts_staged", payload)
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
			timeoutCtx := withNodeTimeoutDuration(nodeCtx, timeout)
			timeoutCtx = withNodeParentContext(timeoutCtx, nodeCtx)
			attemptCtx, cancel = newNodeTimeoutContext(timeoutCtx, timeout)
		}
		out, aerr := runJobBody(attemptCtx, node)
		if aerr == nil {
			if vfn := node.Verifier(); vfn != nil {
				if verr := runVerify(attemptCtx, vfn); verr != nil {
					aerr = &sparkwing.VerifyError{Err: verr}
					nlog.Log("error", aerr.Error())
				}
			}
		}
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
			lastErr = fmt.Errorf("timeout exceeded (%s): %w", timeout, aerr)
			timedOut = true
		}
		lastTimeout = timedOut
	}

done:
	for i, hook := range node.AfterRunHooks() {
		sparkwing.Debug(nodeCtx, "hook: AfterRun[%d] firing (err=%v)", i, lastErr)
		callAfterRun(nodeCtx, hook, lastErr, i, nlog)
	}

	if fatal := nodeLogFatal(nlog); fatal != nil {
		wrapped := fmt.Errorf("logs append blocked; failing node: %w", fatal)
		emitNodeEnd(sparkwing.Failed, wrapped.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), wrapped.Error(), nil, store.FailureLogsAuth, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(wrapped.Error()))
		return nil, wrapped
	}

	if lastErr != nil {
		reason := store.FailureUnknown
		var ve *sparkwing.VerifyError
		switch {
		case errors.As(lastErr, &ve):
			reason = store.FailureVerify
		case lastTimeout:
			reason = store.FailureTimeout
		}
		emitNodeEnd(sparkwing.Failed, lastErr.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), lastErr.Error(), nil, reason, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(lastErr.Error()))
		return nil, lastErr
	}

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

	if digest, perr := r.publishArtifacts(nodeCtx, node); perr != nil {
		wrapped := fmt.Errorf("publish artifacts: %w", perr)
		emitNodeEnd(sparkwing.Failed, wrapped.Error())
		_ = r.backends.State.FinishNodeWithReason(ctx, runID, node.ID(), string(sparkwing.Failed), wrapped.Error(), nil, store.FailureUnknown, nil)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_failed", []byte(wrapped.Error()))
		return nil, wrapped
	} else if digest != "" {
		if serr := r.backends.State.SetNodeArtifactManifest(ctx, runID, node.ID(), digest); serr != nil {
			sparkwing.Debug(nodeCtx, "set artifact manifest: %v", serr)
		}
		payload, _ := json.Marshal(map[string]any{"manifest_digest": digest})
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "artifacts_published", payload)
	}

	emitNodeEnd(sparkwing.Success, "")
	_ = r.backends.State.FinishNode(ctx, runID, node.ID(), string(sparkwing.Success), "", outBytes)
	_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "node_succeeded", nil)

	return output, nil
}

// publishArtifacts captures the node's declared output globs from its
// workspace into the artifact store and returns the resulting manifest
// digest. Returns "" with no error when the node declares no outputs or
// no artifact store is configured. A capture failure (an unreadable or
// unresolvable declared file) fails the node: a producer that promised
// outputs it cannot deliver has not succeeded.
func (r *InProcessRunner) publishArtifacts(ctx context.Context, node *sparkwing.JobNode) (string, error) {
	globs := node.OutputGlobs()
	if len(globs) == 0 || r.backends.Artifact == nil {
		return "", nil
	}
	workspace := nodeWorkspace()
	if workspace == "" {
		return "", fmt.Errorf("no workspace directory to resolve outputs against")
	}
	return captureArtifacts(ctx, r.backends.Artifact, workspace, globs)
}

// stageArtifacts materializes, before the node runs, the artifacts of
// every producer the node consumes (see [sparkwing.JobNode.Consumes]).
// Returns the number of files staged. A no-op when the node consumes
// nothing or no artifact store is configured.
func (r *InProcessRunner) stageArtifacts(ctx context.Context, runID string, node *sparkwing.JobNode) (int, error) {
	edges := node.ConsumeEdges()
	if len(edges) == 0 || r.backends.Artifact == nil {
		return 0, nil
	}
	workspace := nodeWorkspace()
	if workspace == "" {
		return 0, fmt.Errorf("no workspace directory to stage consumed artifacts into")
	}
	return stageConsumedArtifacts(ctx, r.backends.Artifact, r.backends.State, runID, workspace, edges)
}

// nodeWorkspace resolves the directory a node's artifacts are captured
// from and staged into: the runtime work dir, falling back to the
// process cwd, or "" when neither resolves.
func nodeWorkspace() string {
	if ws := sparkwing.CurrentRuntime().WorkDir; ws != "" {
		return ws
	}
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return ""
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

// runVerify runs a node's Verify postcondition with panic recovery. A
// panic is reported as a verify failure, not a runner crash.
func runVerify(ctx context.Context, fn sparkwing.VerifyFn) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx)
}

func (r *InProcessRunner) markSkipped(ctx context.Context, runID, nodeID, reason string) {
	writeCtx := context.WithoutCancel(ctx)
	_ = r.backends.State.FinishNode(writeCtx, runID, nodeID, string(sparkwing.Skipped), reason, nil)
	_ = r.backends.State.AppendEvent(writeCtx, runID, nodeID, "node_skipped", []byte(reason))
}

func (r *InProcessRunner) markFailed(ctx context.Context, runID, nodeID string, reason error) {
	writeCtx := context.WithoutCancel(ctx)
	_ = r.backends.State.FinishNode(writeCtx, runID, nodeID, string(sparkwing.Failed), reason.Error(), nil)
	_ = r.backends.State.AppendEvent(writeCtx, runID, nodeID, "node_failed", []byte(reason.Error()))
}
