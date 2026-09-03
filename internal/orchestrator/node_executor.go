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

type nodeClaimHolderKey struct{}

func withNodeClaimHolder(ctx context.Context, holderID string) context.Context {
	return context.WithValue(ctx, nodeClaimHolderKey{}, holderID)
}

func nodeClaimHolder(ctx context.Context) (string, bool) {
	holder, ok := ctx.Value(nodeClaimHolderKey{}).(string)
	return holder, ok && holder != ""
}

type executionStartAcknowledger interface {
	AcknowledgeNodeExecutionStart(context.Context, string, string, string) error
}

type NodeExecutor struct {
	backends Backends
	labels   []string

	spawn runner.Runner
}

func NewNodeExecutor(backends Backends) *NodeExecutor {
	return &NodeExecutor{backends: backends, labels: []string{"local"}}
}

func (r *NodeExecutor) AdvertisedLabels() []string {
	out := make([]string, len(r.labels))
	copy(out, r.labels)
	return out
}

func (r *NodeExecutor) SetLabels(labels []string) {
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
	_ runner.Runner          = (*NodeExecutor)(nil)
	_ runner.LabelAdvertiser = (*NodeExecutor)(nil)
)

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

func wrapNodeError(nodeID string, err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), nodeID+":") {
		return err
	}
	return fmt.Errorf("%s: %w", nodeID, err)
}

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

func (r *NodeExecutor) RunNode(ctx context.Context, req runner.Request) runner.Result {
	node := req.Node
	if node == nil {
		return runner.Result{
			Outcome: sparkwing.Failed,
			Err:     fmt.Errorf("NodeExecutor: Request.Node is nil for %s/%s", req.RunID, req.NodeID),
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
		nodeID := req.NodeID
		if nodeID == "" {
			nodeID = node.ID()
		}
		r.markFailedIfUnfinished(ctx, req.RunID, nodeID, err)
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

func (r *NodeExecutor) executeNodeWithAdmission(ctx context.Context, req runner.Request) (any, error) {
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
	priority := localAdmissionPriorityFromContext(ctx)
	lease, err := la.admitNode(ctx, r.backends, req.Pipeline, req.RunID, nodeID, req.Node, priority)
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
	childToken := localAdmissionChildTokenFromContext(ctx)
	if childToken == "" {
		childToken = lease.token
	}
	nodeCtx := withLocalAdmission(ctx, la, lease.token, childToken, lease.hostAdmitted, priority)
	return r.executeNode(nodeCtx, req.RunID, req.Node, req.Delegate)
}

func (r *NodeExecutor) WithSpawner(spawner runner.Runner) *NodeExecutor {
	r.spawn = spawner
	return r
}

func (r *NodeExecutor) executeNode(ctx context.Context, runID string, node *sparkwing.JobNode, delegate sparkwing.Logger) (any, error) {
	if r.spawn != nil {
		return r.executeNodeElsewhere(ctx, runID, node, delegate)
	}
	return r.executeNodeInProcess(ctx, runID, node, delegate)
}

func (r *NodeExecutor) executeNodeElsewhere(ctx context.Context, runID string, node *sparkwing.JobNode, delegate sparkwing.Logger) (any, error) {
	res := r.spawn.RunNode(ctx, runner.Request{
		RunID:    runID,
		NodeID:   node.ID(),
		Node:     node,
		Delegate: delegate,
	})
	recordNodeUsage(ctx, r.backends.State, runID, node.ID(), res.Usage)
	switch res.Outcome {
	case sparkwing.Success, sparkwing.Cached:
		return res.Output, nil
	}
	if res.Err != nil {
		return nil, res.Err
	}
	return nil, fmt.Errorf("%s: node spawner returned outcome %q with no error", node.ID(), res.Outcome)
}

func (r *NodeExecutor) executeNodeInProcess(ctx context.Context, runID string, node *sparkwing.JobNode, delegate sparkwing.Logger) (any, error) {
	writeCtx := context.WithoutCancel(ctx)
	nlog, err := r.backends.Logs.OpenNodeLog(runID, node.ID(), delegate)
	if err != nil {
		return nil, err
	}

	nlog = wrapNodeLogWithAnnotations(nlog, r.backends.State, runID, node.ID())
	nlog = wrapNodeLogWithSummary(nlog, r.backends.State, runID, node.ID())
	nlog = wrapNodeLogWithStepState(nlog, r.backends.State, runID, node.ID())
	nlog = wrapNodeLogWithMasker(nlog, secrets.MaskerFromContext(ctx))
	defer func() { _ = nlog.Close() }()

	if holderID, ok := nodeClaimHolder(ctx); ok {
		ack, supported := r.backends.State.(executionStartAcknowledger)
		if !supported {
			return nil, errors.New("claimed node backend does not support execution-start acknowledgement")
		}
		if err := ack.AcknowledgeNodeExecutionStart(ctx, runID, node.ID(), holderID); err != nil {
			return nil, fmt.Errorf("acknowledge execution start: %w", err)
		}
	}
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
	detachSampler := nodemetrics.Attach(samplerCtx, stateMetricsSink{
		backend: r.backends.State,
		runID:   runID,
		nodeID:  node.ID(),
	})
	defer func() {
		detachSampler()
		stopSampler()
	}()

	wedgeBudget, err := storeWedgeBudget()
	if err != nil {
		return nil, err
	}
	hbCtx, stopHB := context.WithCancel(ctx)
	go runNodeHeartbeatLoop(hbCtx, 5*time.Second, r.backends.State, runID, node.ID(), wedgeBudget)
	defer stopHB()

	nodeCtx := sparkwingruntime.WithLogger(ctx, nlog)
	nodeCtx = sparkwingruntime.WithNode(nodeCtx, node.ID())
	nodeCtx = sparkwing.WithToolSlotProvider(nodeCtx, r.toolSlotProvider(runID, node.ID(), delegate))
	nodeCtx = sparkwing.WithResourceReporter(nodeCtx, func(s sparkwing.ResourceSample) {
		nodemetrics.AddReportedChildCPU(s.CPUTime)
		_ = r.backends.State.AddNodeMetricSample(ctx, runID, node.ID(), store.MetricSample{
			TS:            time.Now(),
			CPUMillicores: s.CPUMillicores,
			MemoryBytes:   s.MemoryBytes,
			CPUTime:       s.CPUTime,
		})
	})

	if err := r.writeDispatchSnapshot(nodeCtx, runID, node); err != nil {
		sparkwing.Debug(nodeCtx, "dispatch snapshot: %v", err)
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "dispatch_snapshot_failed", []byte(err.Error()))
	}

	if staged, serr := r.stageArtifacts(nodeCtx, runID, node); serr != nil {
		wrapped := fmt.Errorf("stage consumed artifacts: %w", serr)
		nlog.Log("error", wrapped.Error())
		text := boundedFailureText(ctx, runID, node.ID(), wrapped)
		emitNodeEnd(sparkwing.Failed, text)
		fctx := failureWriteCtx(ctx, wrapped)
		_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, store.FailureUnknown, nil)
		_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
		appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), wrapped)
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
			text := boundedFailureText(ctx, runID, node.ID(), wrapped)
			emitNodeEnd(sparkwing.Failed, text)
			fctx := failureWriteCtx(ctx, wrapped)
			_ = r.backends.State.FinishNode(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil)
			_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
			appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), wrapped)
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
	noProgressTimeout := node.NoProgressTimeoutDuration()

	var output any
	var lastErr error
	var lastTimeout bool
	var lastNoProgressTimeout bool
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
		var cancels []context.CancelFunc
		var absoluteTimeout *nodeTimeoutController
		if timeout > 0 {
			timeoutCtx := withNodeTimeoutDuration(nodeCtx, timeout)
			timeoutCtx = withNodeParentContext(timeoutCtx, nodeCtx)
			var cancel context.CancelFunc
			attemptCtx, cancel = newNodeTimeoutContext(timeoutCtx, timeout)
			absoluteTimeout = nodeTimeoutControllerFromContext(attemptCtx)
			cancels = append(cancels, cancel)
		}
		var progressTimeout *progressTimeoutController
		if noProgressTimeout > 0 {
			var cancel context.CancelFunc
			attemptCtx, progressTimeout, cancel = newProgressTimeoutContext(attemptCtx, noProgressTimeout)
			attemptCtx = sparkwingruntime.WithLogger(attemptCtx, progressLogger{delegate: nlog, progress: progressTimeout})
			cancels = append(cancels, cancel)
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
		absoluteTimedOut := absoluteTimeout != nil && absoluteTimeout.timedOut()
		noProgressTimedOut := progressTimeout != nil && progressTimeout.timedOut()
		for i := len(cancels) - 1; i >= 0; i-- {
			cancels[i]()
		}
		if noProgressTimedOut {
			if aerr == nil {
				aerr = context.DeadlineExceeded
			}
			aerr = fmt.Errorf("no progress for %s: %w", noProgressTimeout, aerr)
		} else if absoluteTimedOut {
			if aerr == nil {
				aerr = context.DeadlineExceeded
			}
			aerr = fmt.Errorf("timeout exceeded (%s): %w", timeout, aerr)
		}
		if aerr == nil {
			output = out
			lastErr = nil
			break
		}
		lastErr = aerr
		lastTimeout = absoluteTimedOut
		lastNoProgressTimeout = noProgressTimedOut
	}

done:
	for i, hook := range node.AfterRunHooks() {
		sparkwing.Debug(nodeCtx, "hook: AfterRun[%d] firing (err=%v)", i, lastErr)
		callAfterRun(nodeCtx, hook, lastErr, i, nlog)
	}

	if fatal := nodeLogFatal(nlog); fatal != nil {
		wrapped := fmt.Errorf("logs append blocked; failing node: %w", fatal)
		text := boundedFailureText(ctx, runID, node.ID(), wrapped)
		emitNodeEnd(sparkwing.Failed, text)
		fctx := failureWriteCtx(ctx, wrapped)
		_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, store.FailureLogsAuth, nil)
		_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
		appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), wrapped)
		return nil, wrapped
	}

	if lastErr != nil {
		reason := store.FailureUnknown
		var ve *sparkwing.VerifyError
		switch {
		case lastNoProgressTimeout:
			reason = store.FailureNoProgressTimeout
		case lastTimeout:
			reason = store.FailureTimeout
		case errors.As(lastErr, &ve):
			reason = store.FailureVerify
		}
		text := boundedFailureText(ctx, runID, node.ID(), lastErr)
		emitNodeEnd(sparkwing.Failed, text)
		fctx := failureWriteCtx(ctx, lastErr)
		_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, reason, nil)
		_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
		appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), lastErr)
		return nil, lastErr
	}

	if count, reason := nodeLogDrops(nlog); count > 0 {
		payload, _ := json.Marshal(map[string]any{"count": count, "reason": reason})
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "logs_drop", payload)
		if logsDropIsFatal() {
			dropped := droppedLogsError(count, reason)
			text := boundedFailureText(ctx, runID, node.ID(), dropped)
			emitNodeEnd(sparkwing.Failed, text)
			fctx := failureWriteCtx(ctx, dropped)
			_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, store.FailureLogsDropped, nil)
			_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
			appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), dropped)
			return nil, dropped
		}
	}

	var outBytes []byte
	if output != nil {
		b, merr := json.Marshal(output)
		if merr != nil {
			wrapped := nodeOutputMarshalError(node.ID(), output, merr)
			nlog.Log("error", wrapped.Error())
			text := boundedFailureText(ctx, runID, node.ID(), wrapped)
			emitNodeEnd(sparkwing.Failed, text)
			fctx := failureWriteCtx(ctx, wrapped)
			_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, store.FailureUnknown, nil)
			_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
			appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), wrapped)
			return nil, wrapped
		}
		outBytes = b
	}

	if digest, perr := r.publishArtifacts(nodeCtx, node); perr != nil {
		wrapped := fmt.Errorf("publish artifacts: %w", perr)
		text := boundedFailureText(ctx, runID, node.ID(), wrapped)
		emitNodeEnd(sparkwing.Failed, text)
		fctx := failureWriteCtx(ctx, wrapped)
		_ = r.backends.State.FinishNodeWithReason(fctx, runID, node.ID(), string(sparkwing.Failed), text, nil, store.FailureUnknown, nil)
		_ = r.backends.State.AppendEvent(fctx, runID, node.ID(), "node_failed", []byte(text))
		appendFailureExcerptEvent(fctx, r.backends.State, runID, node.ID(), wrapped)
		return nil, wrapped
	} else if digest != "" {
		if serr := r.backends.State.SetNodeArtifactManifest(writeCtx, runID, node.ID(), digest); serr != nil {
			sparkwing.Debug(nodeCtx, "set artifact manifest: %v", serr)
		}
		payload, _ := json.Marshal(map[string]any{"manifest_digest": digest})
		_ = r.backends.State.AppendEvent(ctx, runID, node.ID(), "artifacts_published", payload)
	}

	emitNodeEnd(sparkwing.Success, "")
	_ = r.backends.State.FinishNode(writeCtx, runID, node.ID(), string(sparkwing.Success), "", outBytes)
	_ = r.backends.State.AppendEvent(writeCtx, runID, node.ID(), "node_succeeded", nil)

	if outBytes == nil {
		return nil, nil
	}
	return outBytes, nil
}

func (r *NodeExecutor) publishArtifacts(ctx context.Context, node *sparkwing.JobNode) (string, error) {
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

func (r *NodeExecutor) stageArtifacts(ctx context.Context, runID string, node *sparkwing.JobNode) (int, error) {
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

func nodeWorkspace() string {
	if ws := sparkwing.CurrentRuntime().WorkDir; ws != "" {
		return ws
	}
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return ""
}

func nodeLogFatal(nlog NodeLog) error {
	if f, ok := nlog.(interface{ Fatal() error }); ok {
		return f.Fatal()
	}
	return nil
}

const LogsDropPolicyEnvVar = "SPARKWING_LOGS_DROP_POLICY"

func droppedLogsError(count int, reason string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d log line(s) lost: the log store stayed unreachable past the append retry budget", count)
	b.WriteString("\n  check: the logs backend this run named in invocation.backends")
	b.WriteString("\n         (for s3: the bucket, AWS_REGION, credentials, SPARKWING_S3_ENDPOINT)")

	if strings.Contains(reason, "404") {
		b.WriteString("\n  note:  the store answered 404, so nothing serves log appends at that URL.")
		b.WriteString("\n         A controller does not; sparkwing-logs is a separate service.")
	}
	fmt.Fprintf(&b, "\n  keep such runs green with %s=warn", LogsDropPolicyEnvVar)
	if reason != "" {
		fmt.Fprintf(&b, "\n  cause: %s", reason)
	}
	return errors.New(b.String())
}

func logsDropIsFatal() bool {
	return os.Getenv(LogsDropPolicyEnvVar) != "warn"
}

func nodeLogDrops(nlog NodeLog) (int, string) {
	if d, ok := nlog.(interface{ Drops() (int, string) }); ok {
		return d.Drops()
	}
	return 0, ""
}

func runVerify(ctx context.Context, fn sparkwing.VerifyFn) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx)
}

func (r *NodeExecutor) markSkipped(ctx context.Context, runID, nodeID, reason string) {
	writeCtx := context.WithoutCancel(ctx)
	_ = r.backends.State.FinishNode(writeCtx, runID, nodeID, string(sparkwing.Skipped), reason, nil)
	_ = r.backends.State.AppendEvent(writeCtx, runID, nodeID, "node_skipped", []byte(reason))
}

func (r *NodeExecutor) markFailed(ctx context.Context, runID, nodeID string, reason error) {
	writeCtx := context.WithoutCancel(ctx)
	text := boundedFailureText(ctx, runID, nodeID, reason)
	_ = r.backends.State.FinishNode(writeCtx, runID, nodeID, string(sparkwing.Failed), text, nil)
	_ = r.backends.State.AppendEvent(writeCtx, runID, nodeID, "node_failed", []byte(text))
	appendFailureExcerptEvent(writeCtx, r.backends.State, runID, nodeID, reason)
}

func failureWriteCtx(ctx context.Context, err error) context.Context {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return ctx
	}
	return context.WithoutCancel(ctx)
}

func (r *NodeExecutor) markFailedIfUnfinished(ctx context.Context, runID, nodeID string, reason error) {
	if ctx.Err() != nil && errors.Is(reason, context.Canceled) {
		return
	}
	writeCtx := context.WithoutCancel(ctx)
	n, err := r.backends.State.GetNode(writeCtx, runID, nodeID)
	if err != nil {
		return
	}
	if n != nil && n.Outcome != "" {
		return
	}
	r.markFailed(writeCtx, runID, nodeID, reason)
}
