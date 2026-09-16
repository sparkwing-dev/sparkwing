package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type childAwaitPollPolicy interface {
	success()
	failure(context.Context, string, string, error, bool) error
}

type childAwaitDiagnostics interface {
	info(context.Context, string, ...any)
	warn(context.Context, string, error, ...any)
	appendEvent(context.Context, StateBackend, string, string, string, []byte)
}

type childAwaitConfig struct {
	state       StateBackend
	concurrency ConcurrencyBackend

	parentRunID string
	retryOf     string
	triggerEnv  func(context.Context) map[string]string
	masker      *secrets.Masker

	localAdmission      *LocalAdmission
	watchdogWaits       *admissionWaitTracker
	watchdogParticipant string

	pollFactory       func() (childAwaitPollPolicy, error)
	heartbeatInterval time.Duration
	diagnostics       childAwaitDiagnostics
}

func (c childAwaitConfig) await(ctx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
	resumeProgressTimeout := pauseProgressTimeout(ctx)
	defer resumeProgressTimeout()

	currentNode := sparkwing.NodeFromContext(ctx)
	var childRetryOf string
	if c.retryOf != "" && currentNode != "" {
		id, err := c.state.FindSpawnedChildTriggerID(ctx, c.retryOf, currentNode, req.Pipeline)
		if err != nil {
			c.warn(ctx, "find prior spawned child for retry chain", err,
				"run_id", c.parentRunID, "node", currentNode, "pipeline", req.Pipeline)
		} else {
			childRetryOf = id
		}
	}

	var triggerEnv map[string]string
	if c.triggerEnv != nil {
		triggerEnv = c.triggerEnv(ctx)
	}
	childRunID, err := enqueueTriggerWithEnv(ctx, c.state,
		req.Pipeline, req.Args, c.parentRunID, currentNode, childRetryOf,
		"await-pipeline", "", req.Repo, req.Branch, triggerEnv)
	if err != nil {
		return nil, fmt.Errorf("enqueue trigger: %w", err)
	}

	awaitBounded := childAwaitBounded(ctx, req.Timeout)
	if awaitBounded && c.watchdogWaits != nil && c.watchdogParticipant != "" {
		c.watchdogWaits.begin(c.watchdogParticipant)
		defer c.watchdogWaits.end(c.watchdogParticipant)
	}
	c.info(ctx, "spawned child run",
		"run_id", c.parentRunID, "node", currentNode,
		"child_run_id", childRunID, "pipeline", req.Pipeline, "repo", req.Repo)

	startedAt := time.Now()
	emitChildFinish := func(status, errMsg string) {
		if currentNode == "" {
			return
		}
		attrs := map[string]any{
			"child_run_id": childRunID,
			"pipeline":     req.Pipeline,
			"status":       status,
			"duration_ms":  time.Since(startedAt).Milliseconds(),
		}
		if errMsg != "" {
			attrs["error"] = errMsg
		}
		payload, marshalErr := json.Marshal(attrs)
		if marshalErr != nil {
			c.warn(ctx, "child_run_finish audit event encode failed", marshalErr,
				"run_id", c.parentRunID, "node", currentNode,
				"child_run_id", childRunID, "pipeline", req.Pipeline)
			return
		}
		payload = maskEventPayload(c.masker, payload)
		c.diagnostics.appendEvent(context.WithoutCancel(ctx), c.state, c.parentRunID, currentNode,
			"child_run_finish", payload)
	}

	if currentNode != "" {
		payload, marshalErr := json.Marshal(map[string]any{
			"child_run_id":    childRunID,
			"pipeline":        req.Pipeline,
			"node_id":         req.NodeID,
			"args":            req.Args,
			"timeout_seconds": int64(req.Timeout.Seconds()),
		})
		if marshalErr != nil {
			c.warn(ctx, "child_run_start audit event encode failed", marshalErr,
				"run_id", c.parentRunID, "node", currentNode,
				"child_run_id", childRunID, "pipeline", req.Pipeline)
		} else {
			payload = maskEventPayload(c.masker, payload)
			c.diagnostics.appendEvent(ctx, c.state, c.parentRunID, currentNode,
				"child_run_start", payload)
		}
	}

	pollCtx := ctx
	parentCtx := nodeParentContextFromContext(ctx)
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		pollCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	timeoutPausedForAdmission := false
	timeoutAdjustedForAdmission := false
	watchdogPausedForAdmission := false
	var admissionStatusErr error
	nodeTimeout := nodeTimeoutControllerFromContext(ctx)
	var admissionMu sync.Mutex
	defer func() {
		admissionMu.Lock()
		paused := watchdogPausedForAdmission
		watchdogPausedForAdmission = false
		admissionMu.Unlock()
		if paused && c.watchdogWaits != nil {
			c.watchdogWaits.end(c.watchdogParticipant)
		}
	}()
	updateTimeoutForAdmission := func(statusCtx context.Context) bool {
		trackNodeTimeout := req.Timeout == 0 && nodeTimeout != nil && nodeTimeoutDurationFromContext(ctx) > 0
		trackWatchdogAdmission := !awaitBounded && c.watchdogWaits != nil && c.watchdogParticipant != ""
		if (!trackNodeTimeout && !trackWatchdogAdmission) || statusCtx.Err() != nil {
			return false
		}
		admission, statusErr := childAdmissionStatus(statusCtx, c.state, c.concurrency, c.localAdmission, childRunID)
		admissionMu.Lock()
		defer admissionMu.Unlock()
		if timeoutAdjustedForAdmission {
			return false
		}
		if statusErr != nil {
			if timeoutPausedForAdmission || watchdogPausedForAdmission {
				admissionStatusErr = statusErr
			}
			return false
		}
		if trackWatchdogAdmission {
			switch admission.Status {
			case childPlanAdmissionQueued:
				if !watchdogPausedForAdmission {
					c.watchdogWaits.begin(c.watchdogParticipant)
					watchdogPausedForAdmission = true
				}
			case childPlanAdmissionAdmitted:
				if watchdogPausedForAdmission {
					c.watchdogWaits.end(c.watchdogParticipant)
					watchdogPausedForAdmission = false
				}
			}
		}
		if !trackNodeTimeout {
			return false
		}
		switch admission.Status {
		case childPlanAdmissionQueued:
			if timeoutPausedForAdmission {
				return true
			}
			if nodeTimeout.pauseAt(admission.QueuedAt) {
				timeoutPausedForAdmission = true
				c.info(ctx, "child run queued for plan admission; pausing parent node timeout until admission",
					"run_id", c.parentRunID, "node", currentNode,
					"child_run_id", childRunID, "pipeline", req.Pipeline)
				return true
			}
		case childPlanAdmissionAdmitted:
			if timeoutPausedForAdmission {
				if nodeTimeout.resumeAt(admission.AdmittedAt) {
					timeoutPausedForAdmission = false
					timeoutAdjustedForAdmission = true
					c.info(ctx, "child run left plan admission; parent node timeout resumed",
						"run_id", c.parentRunID, "node", currentNode,
						"child_run_id", childRunID, "pipeline", req.Pipeline)
					return true
				}
				return false
			}
			if admission.QueuedAt.IsZero() || admission.AdmittedAt.IsZero() {
				return false
			}
			if nodeTimeout.accountCompletedAdmission(admission.QueuedAt, admission.AdmittedAt) {
				timeoutAdjustedForAdmission = true
				c.info(ctx, "child run completed plan admission; parent node timeout adjusted",
					"run_id", c.parentRunID, "node", currentNode,
					"child_run_id", childRunID, "pipeline", req.Pipeline)
				return true
			}
		}
		return false
	}
	admissionPauseActive := func() bool {
		admissionMu.Lock()
		defer admissionMu.Unlock()
		return timeoutPausedForAdmission || watchdogPausedForAdmission
	}
	currentAdmissionStatusErr := func() error {
		admissionMu.Lock()
		defer admissionMu.Unlock()
		return admissionStatusErr
	}
	admissionDeadlineHandled := func() bool {
		inspectCtx, cancel := context.WithTimeout(parentCtx, childAdmissionInspectorTimeout)
		defer cancel()
		return updateTimeoutForAdmission(inspectCtx)
	}
	if nodeTimeout != nil && req.Timeout == 0 {
		clearInspector := nodeTimeout.setDeadlineInspector(admissionDeadlineHandled)
		defer clearInspector()
	}

	pollPolicy, err := c.pollFactory()
	if err != nil {
		return nil, err
	}

	var heartbeat <-chan time.Time
	if c.heartbeatInterval > 0 {
		ticker := time.NewTicker(c.heartbeatInterval)
		defer ticker.Stop()
		heartbeat = ticker.C
	}
	awaitObs := childAwaitObserver{startedAt: startedAt}
	awaitTimeout := func(cause error) error {
		awaitObs.admissionOff = admissionPauseActive()
		return fmt.Errorf("waiting for child %s: %w (%s)", childRunID, cause, awaitObs.evidence())
	}
	lastStatus := "pending"
	for {
		updateTimeoutForAdmission(parentCtx)
		if statusErr := currentAdmissionStatusErr(); statusErr != nil {
			emitChildFinish("failed", statusErr.Error())
			return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
		}
		run, runErr := c.state.GetRun(pollCtx, childRunID)
		if runErr != nil {
			if errors.Is(runErr, store.ErrNotFound) {
				awaitObs.observeMissing()
				pollPolicy.success()
			} else {
				awaitObs.observeError(runErr)
				if terminal := pollPolicy.failure(ctx, childRunID, req.Pipeline, runErr, awaitObs.firstError()); terminal != nil {
					emitChildFinish("failed", terminal.Error())
					return nil, terminal
				}
			}
		} else {
			pollPolicy.success()
			awaitObs.observeStatus(run.Status)
			lastStatus = run.Status
			switch run.Status {
			case "success":
				updateTimeoutForAdmission(parentCtx)
				if pollErr := pollCtx.Err(); pollErr != nil {
					timeoutErr := awaitTimeout(pollErr)
					emitChildFinish("timeout", timeoutErr.Error())
					return nil, timeoutErr
				}
				if deadline, ok := pollCtx.Deadline(); ok && time.Now().After(deadline) {
					timeoutErr := awaitTimeout(context.DeadlineExceeded)
					emitChildFinish("timeout", timeoutErr.Error())
					return nil, timeoutErr
				}
				emitChildFinish("success", "")
				if req.NodeID == "" {
					return &sparkwing.ResolvedPipelineRef{RunID: childRunID}, nil
				}
				data, outputErr := c.state.GetNodeOutput(pollCtx, childRunID, req.NodeID)
				if outputErr != nil {
					return nil, fmt.Errorf("get child %s/%s output: %w", childRunID, req.NodeID, outputErr)
				}
				return &sparkwing.ResolvedPipelineRef{RunID: childRunID, Data: data}, nil
			case "failed":
				emitChildFinish("failed", run.Error)
				return nil, fmt.Errorf("child run %s failed: %s", childRunID, run.Error)
			case "cancelled":
				emitChildFinish("cancelled", "")
				return nil, fmt.Errorf("child run %s was cancelled", childRunID)
			}
		}
		updateTimeoutForAdmission(parentCtx)
		if statusErr := currentAdmissionStatusErr(); statusErr != nil {
			emitChildFinish("failed", statusErr.Error())
			return nil, fmt.Errorf("child %s plan admission status: %w", childRunID, statusErr)
		}
		select {
		case <-pollCtx.Done():
			updateTimeoutForAdmission(parentCtx)
			timeoutErr := awaitTimeout(pollCtx.Err())
			emitChildFinish("timeout", timeoutErr.Error())
			return nil, timeoutErr
		case <-heartbeat:
			c.info(ctx, "still waiting on child run",
				"run_id", c.parentRunID, "node", currentNode,
				"child_run_id", childRunID, "pipeline", req.Pipeline,
				"status", lastStatus, "elapsed", time.Since(startedAt).Round(time.Second))
		case <-time.After(childAwaitPollInterval(pollCtx, admissionPauseActive())):
		}
	}
}

func (c childAwaitConfig) info(ctx context.Context, message string, attrs ...any) {
	if c.diagnostics != nil {
		c.diagnostics.info(ctx, message, attrs...)
	}
}

func (c childAwaitConfig) warn(ctx context.Context, message string, err error, attrs ...any) {
	if c.diagnostics != nil {
		c.diagnostics.warn(ctx, message, err, attrs...)
	}
}

type localChildAwaitDiagnostics struct{}

func (localChildAwaitDiagnostics) info(ctx context.Context, message string, attrs ...any) {
	sparkwing.LoggerFromContext(ctx).Emit(sparkwing.LogRecord{
		Level: "info", Msg: message, Attrs: childAwaitAttrs(attrs),
	})
}

func (localChildAwaitDiagnostics) warn(ctx context.Context, message string, err error, attrs ...any) {
	fields := childAwaitAttrs(attrs)
	fields["error"] = err.Error()
	sparkwing.LoggerFromContext(ctx).Emit(sparkwing.LogRecord{
		Level: "warn", Msg: message, Attrs: fields,
	})
}

func (localChildAwaitDiagnostics) appendEvent(
	ctx context.Context, state StateBackend, runID, nodeID, kind string, payload []byte,
) {
	noteEvent(ctx, state, runID, nodeID, kind, payload)
}

type podChildAwaitDiagnostics struct {
	logger *slog.Logger
}

func (d podChildAwaitDiagnostics) info(ctx context.Context, message string, attrs ...any) {
	d.logger.InfoContext(ctx, message, attrs...)
}

func (d podChildAwaitDiagnostics) warn(ctx context.Context, message string, err error, attrs ...any) {
	d.logger.WarnContext(ctx, message, append(attrs, "err", err)...)
}

func (d podChildAwaitDiagnostics) appendEvent(
	ctx context.Context, state StateBackend, runID, nodeID, kind string, payload []byte,
) {
	if err := state.AppendEvent(ctx, runID, nodeID, kind, payload); err != nil {
		d.logger.WarnContext(ctx, "child run audit event append failed",
			"write", kind, "run_id", runID, "node", nodeID, "err", err)
	}
}

func childAwaitAttrs(attrs []any) map[string]any {
	fields := make(map[string]any, len(attrs)/2)
	for i := 0; i+1 < len(attrs); i += 2 {
		key, ok := attrs[i].(string)
		if ok {
			fields[key] = attrs[i+1]
		}
	}
	return fields
}

type wedgeChildAwaitPoll struct {
	guard *storeWedgeGuard
}

func (p *wedgeChildAwaitPoll) success() { p.guard.success() }

func (p *wedgeChildAwaitPoll) failure(_ context.Context, childRunID, _ string, err error, _ bool) error {
	return p.guard.fail(fmt.Sprintf("waiting for child run %s", childRunID), err)
}

type retryChildAwaitPoll struct {
	diagnostics childAwaitDiagnostics
	runID       string
	nodeID      string
}

func (*retryChildAwaitPoll) success() {}

func (p *retryChildAwaitPoll) failure(ctx context.Context, childRunID, pipeline string, err error, first bool) error {
	if first && p.diagnostics != nil {
		p.diagnostics.warn(ctx, "child run status poll failed; retrying", err,
			"run_id", p.runID, "node", p.nodeID,
			"child_run_id", childRunID, "pipeline", pipeline)
	}
	return nil
}
