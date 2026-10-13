package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var approvalPoll = 500 * time.Millisecond

// safety: a store whose every read fails for this long is wedged, not busy, so the gate fails.
var approvalStoreBudget = 5 * time.Minute

// safety: the bare store's AppendEvent has another shape, so a run on it records no approval event.
type eventAppender interface {
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error
}

// safety: a gate never reaches the inner layers, so it holds no admission
// lease, starts no process and runs no envelope while a human decides.
func approvalsLayer(cfg *Config, next Executor) Executor {
	return func(ctx context.Context, n *NodeRun) Result {
		if n.Plan.Approval == nil {
			return next(ctx, n)
		}
		return approvalGate(ctx, cfg, n)
	}
}

func approvalGate(ctx context.Context, cfg *Config, n *NodeRun) Result {
	run, id, gate := n.Run, n.Plan.ID, n.Plan.Approval
	writeCtx := context.WithoutCancel(ctx)
	emit := func(sparkwing.LogRecord) {}
	if a := cfg.Attempts; a != nil && a.OpenLog != nil {
		nlog, err := a.OpenLog(ctx, run.ID, id)
		if err != nil {
			return failed(fmt.Errorf("open node log: %w", err))
		}
		defer func() {
			if err := nlog.Close(); err != nil {
				run.Logger.Warn("nodehost: close node log", "run", run.ID, "node", id, "err", err)
			}
		}()
		emit = func(rec sparkwing.LogRecord) {
			rec.Msg = a.Masker.Mask(rec.Msg)
			rec.Attrs = a.Masker.MaskAttrs(rec.Attrs)
			nlog.Emit(rec)
		}
	}
	if err := run.State.StartNode(writeCtx, run.ID, id); err != nil {
		return failed(fmt.Errorf("start node %s: %w", id, err))
	}
	start := time.Now()
	onTimeout := gate.OnTimeout
	if onTimeout == "" {
		onTimeout = store.ApprovalOnTimeoutFail
	}
	appr := store.Approval{
		RunID: run.ID, NodeID: id, RequestedAt: start,
		Message: gate.Message, TimeoutMS: gate.TimeoutMS, OnTimeout: onTimeout,
	}
	if err := run.State.CreateApproval(writeCtx, appr); err != nil {
		return failed(fmt.Errorf("CreateApproval: %w", err))
	}
	if events, ok := run.State.(eventAppender); ok {
		payload, err := json.Marshal(map[string]any{"message": gate.Message, "timeout_ms": gate.TimeoutMS})
		if err == nil {
			err = events.AppendEvent(writeCtx, run.ID, id, "approval_requested", payload)
		}
		if err != nil {
			run.Logger.Warn("nodehost: approval event", "run", run.ID, "node", id, "err", err)
		}
	}
	emit(sparkwing.LogRecord{
		TS: time.Now(), Level: "info", Event: "approval_requested", Msg: gate.Message,
		Attrs: map[string]any{"timeout_ms": gate.TimeoutMS},
	})
	var deadline time.Time
	if gate.TimeoutMS > 0 {
		deadline = start.Add(time.Duration(gate.TimeoutMS) * time.Millisecond)
	}

	r := pollApproval(ctx, run, id, deadline, onTimeout)
	if r.via != "" {
		attrs := map[string]any{"resolution": r.resolution, "via": r.via, "duration_ms": time.Since(start).Milliseconds()}
		if r.approver != "" {
			attrs["approver"] = r.approver
		}
		if r.comment != "" {
			attrs["comment"] = r.comment
		}
		emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "approval_resolved", Msg: r.summary, Attrs: attrs})
		if err := run.State.AppendNodeAnnotation(writeCtx, run.ID, id, r.summary); err != nil {
			run.Logger.Warn("nodehost: approval annotation", "run", run.ID, "node", id, "err", err)
		}
	}
	return r.res
}

type approvalResult struct {
	res                                         Result
	resolution, approver, comment, via, summary string
}

func pollApproval(ctx context.Context, run *Run, id string, deadline time.Time, onTimeout string) approvalResult {
	cancelled := approvalResult{res: Result{Outcome: sparkwing.Cancelled, Err: errors.New("ctx-cancelled")}}
	ticker := time.NewTicker(approvalPoll)
	defer ticker.Stop()
	var deadlineC <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		deadlineC = t.C
	}
	var firstFailure time.Time
	for {
		appr, err := run.State.GetApproval(ctx, run.ID, id)
		switch {
		case ctx.Err() != nil:
			return cancelled
		case err != nil:
			if firstFailure.IsZero() {
				firstFailure = time.Now()
			}
			// safety: a SQLite locking-protocol error never clears by retrying, so it fails the gate at once.
			if store.IsProtocolErr(err) || time.Since(firstFailure) >= approvalStoreBudget {
				return approvalResult{res: failed(fmt.Errorf("approval poll %s/%s: every read for %s failed: %w", run.ID, id, time.Since(firstFailure).Round(time.Second), err))}
			}
		case appr.ResolvedAt != nil:
			return approvalResolved(appr.Resolution, appr.Approver, appr.Comment)
		default:
			firstFailure = time.Time{}
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return approvalExpired(ctx, run, id, onTimeout)
		}
		select {
		case <-ticker.C:
		case <-deadlineC:
		case <-ctx.Done():
			return cancelled
		}
	}
}

// safety: a human resolution that lands just before the timeout's own write
// wins, so the gate never reports a policy outcome over a recorded decision.
func approvalExpired(ctx context.Context, run *Run, id, onTimeout string) approvalResult {
	_, err := run.State.ResolveApproval(context.WithoutCancel(ctx), run.ID, id, store.ApprovalResolutionTimedOut, "sparkwing", "timeout")
	if ctx.Err() != nil {
		return approvalResult{res: Result{Outcome: sparkwing.Cancelled, Err: errors.New("ctx-cancelled")}}
	}
	if errors.Is(err, store.ErrLockHeld) {
		if appr, gerr := run.State.GetApproval(ctx, run.ID, id); gerr == nil && appr.ResolvedAt != nil {
			return approvalResolved(appr.Resolution, appr.Approver, appr.Comment)
		}
	}
	r := approvalResult{via: "timeout-policy:" + onTimeout}
	switch onTimeout {
	case store.ApprovalOnTimeoutApprove:
		r.res.Outcome, r.summary = sparkwing.Success, "auto-approved (timeout policy=approve)"
	case store.ApprovalOnTimeoutDeny:
		r.res = failed(errors.New("approval timed out (policy=deny)"))
		r.summary = "auto-denied (timeout policy=deny)"
	default:
		r.res = failed(errors.New("approval timed out (policy=fail)"))
		r.summary = "approval timed out (policy=fail)"
	}
	return r
}

func approvalResolved(resolution, approver, comment string) approvalResult {
	r := approvalResult{resolution: resolution, approver: approver, comment: comment, via: "human"}
	if approver == "sparkwing" {
		r.via = "timeout"
	}
	switch resolution {
	case store.ApprovalResolutionApproved:
		r.res.Outcome = sparkwing.Success
		r.summary = "approved by " + approver
		if comment != "" {
			r.summary += " · " + comment
		}
	case store.ApprovalResolutionDenied:
		r.summary = "denied by " + approver
		if comment != "" {
			r.summary += ": " + comment
		}
		r.res = failed(errors.New(r.summary))
	case store.ApprovalResolutionTimedOut:
		r.summary = "approval timed out"
		r.res = failed(errors.New(r.summary))
	default:
		r.summary = "unknown approval resolution: " + resolution
		r.res = failed(errors.New(r.summary))
	}
	if payload, err := json.Marshal(map[string]any{"resolution": resolution, "approver": approver, "comment": comment}); err == nil {
		r.res.Output = payload
	}
	return r
}
