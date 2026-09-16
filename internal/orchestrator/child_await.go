package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	infof             func(context.Context, string, ...any)
	warnf             func(context.Context, string, ...any)
}

func (c childAwaitConfig) await(ctx context.Context, req sparkwing.AwaitRequest) (*sparkwing.ResolvedPipelineRef, error) {
	resumeProgressTimeout := pauseProgressTimeout(ctx)
	defer resumeProgressTimeout()

	currentNode := sparkwing.NodeFromContext(ctx)
	var childRetryOf string
	if c.retryOf != "" && currentNode != "" {
		id, err := c.state.FindSpawnedChildTriggerID(ctx, c.retryOf, currentNode, req.Pipeline)
		if err != nil {
			c.warn(ctx, "find prior spawned child for retry chain: %v", err)
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
	c.info(ctx, "spawned child run %s (pipeline=%s%s)", childRunID, req.Pipeline, repoSuffix(req.Repo))

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
			c.warn(ctx, "child_run_finish audit event encode failed: %v", marshalErr)
			return
		}
		payload = maskEventPayload(c.masker, payload)
		if eventErr := c.state.AppendEvent(context.WithoutCancel(ctx), c.parentRunID, currentNode,
			"child_run_finish", payload); eventErr != nil {
			c.warn(ctx, "child_run_finish audit event append failed: %v", eventErr)
		}
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
			c.warn(ctx, "child_run_start audit event encode failed: %v", marshalErr)
		} else {
			payload = maskEventPayload(c.masker, payload)
			if eventErr := c.state.AppendEvent(ctx, c.parentRunID, currentNode,
				"child_run_start", payload); eventErr != nil {
				c.warn(ctx, "child_run_start audit event append failed: %v", eventErr)
			}
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
				c.info(ctx, "child %s [%s] is queued for plan admission; pausing parent node timeout until admission", childRunID, req.Pipeline)
				return true
			}
		case childPlanAdmissionAdmitted:
			if timeoutPausedForAdmission {
				if nodeTimeout.resumeAt(admission.AdmittedAt) {
					timeoutPausedForAdmission = false
					timeoutAdjustedForAdmission = true
					c.info(ctx, "child %s [%s] left plan admission; parent node timeout resumed", childRunID, req.Pipeline)
					return true
				}
				return false
			}
			if admission.QueuedAt.IsZero() || admission.AdmittedAt.IsZero() {
				return false
			}
			if nodeTimeout.accountCompletedAdmission(admission.QueuedAt, admission.AdmittedAt) {
				timeoutAdjustedForAdmission = true
				c.info(ctx, "child %s [%s] completed plan admission; parent node timeout adjusted", childRunID, req.Pipeline)
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
			c.info(ctx, "still waiting on child %s [%s] (status=%s, elapsed=%s)", childRunID, req.Pipeline, lastStatus, time.Since(startedAt).Round(time.Second))
		case <-time.After(childAwaitPollInterval(pollCtx, admissionPauseActive())):
		}
	}
}

func (c childAwaitConfig) info(ctx context.Context, format string, args ...any) {
	if c.infof != nil {
		c.infof(ctx, format, args...)
	}
}

func (c childAwaitConfig) warn(ctx context.Context, format string, args ...any) {
	if c.warnf != nil {
		c.warnf(ctx, format, args...)
	}
}

type wedgeChildAwaitPoll struct {
	guard *storeWedgeGuard
}

func (p *wedgeChildAwaitPoll) success() { p.guard.success() }

func (p *wedgeChildAwaitPoll) failure(_ context.Context, childRunID, _ string, err error, _ bool) error {
	return p.guard.fail(fmt.Sprintf("waiting for child run %s", childRunID), err)
}

type retryChildAwaitPoll struct {
	warnf func(context.Context, string, ...any)
}

func (*retryChildAwaitPoll) success() {}

func (p *retryChildAwaitPoll) failure(ctx context.Context, childRunID, pipeline string, err error, first bool) error {
	if first && p.warnf != nil {
		p.warnf(ctx, "child run status poll failed; retrying (child=%s pipeline=%s): %v", childRunID, pipeline, err)
	}
	return nil
}
