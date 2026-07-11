package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// errNodeQueueTimeout marks a bounded node-level admission wait that
// elapsed without a grant.
var errNodeQueueTimeout = errors.New("queue timeout")

// runNodeUnderDaemonSem runs a node whose concurrency group is
// arbitrated by the local admission daemon: one short-lived,
// semaphores-only acquisition held for the node's execution and
// released at node end. Node timeouts are armed inside executeNode,
// after the grant, so time spent queued for admission is never charged
// against the node's timeout. An eviction pushed while the node runs
// (a cancel_others arrival) cancels execution and finalizes the node
// as superseded, naming the key and the superseding run.
func (r *InProcessRunner) runNodeUnderDaemonSem(ctx context.Context, req runner.Request, la *LocalAdmission, group *sparkwing.ConcurrencyGroup) runner.Result {
	node := req.Node
	limit := group.Limit()
	key := scopedGroupKey(group, req.RunID)
	claim := wingwire.SemaphoreClaim{
		Name:            key,
		Cost:            node.ConcurrencyCost(),
		Capacity:        limit.Capacity,
		Policy:          wingwire.Policy(limit.OnLimit),
		CancelTimeoutMS: limit.CancelTimeout.Milliseconds(),
	}

	acquireCtx := ctx
	if limit.OnLimit == sparkwing.Queue && limit.QueueTimeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeoutCause(ctx, limit.QueueTimeout, errNodeQueueTimeout)
		defer cancel()
	}

	waited := false
	lastDetail := ""
	onQueued := func(q wingwire.Queued) {
		if !waited {
			waited = true
			if req.ReleaseWorkerSlot != nil {
				req.ReleaseWorkerSlot()
			}
			payload, _ := json.Marshal(map[string]any{
				"key":          key,
				"kind":         "queued",
				"position":     q.Position,
				"queue_length": q.QueueLength,
			})
			_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_wait", payload)
		}
		if detail := fmt.Sprintf("queued in %s: %d ahead", key, max(0, q.Position-1)); detail != lastDetail {
			lastDetail = detail
			_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, node.ID(), detail)
			r.emitConcWaitLog(ctx, req, detail)
		}
	}

	lease, err := la.acquireNodeSlot(acquireCtx, req.RunID, node.ID(), claim, onQueued)
	if err != nil {
		return r.failedDaemonAcquire(ctx, acquireCtx, req, key, limit.QueueTimeout, err)
	}

	if waited {
		if req.ReacquireWorkerSlot != nil && !req.ReacquireWorkerSlot() {
			_ = lease.Release()
			r.markFailed(ctx, req.RunID, node.ID(), context.Canceled)
			return runner.Result{Outcome: sparkwing.Cancelled}
		}
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_promoted", nil)
		_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, node.ID(), "")
	}

	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	var evicted atomic.Pointer[wingwire.Evicted]
	go lease.Watch(func(ev wingwire.Evicted) {
		evicted.Store(&ev)
		cancelExec()
	})
	defer func() { _ = lease.Release() }()

	if reason, skip := evalSkipPredicates(execCtx, node); skip {
		r.markSkipped(execCtx, req.RunID, node.ID(), reason)
		return runner.Result{Outcome: sparkwing.Skipped}
	}

	output, err := r.executeNode(execCtx, req.RunID, node, req.Delegate)
	if ev := evicted.Load(); ev != nil {
		serr := fmt.Errorf("concurrency key %q: superseded by run %s under %s", ev.Key, ev.SupersededBy, ev.Policy)
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "node_superseded", []byte(serr.Error()))
		_ = r.backends.State.FinishNode(ctx, req.RunID, node.ID(), string(sparkwing.Superseded), serr.Error(), nil)
		return runner.Result{Outcome: sparkwing.Superseded, Err: serr}
	}
	if err != nil {
		return runner.Result{Outcome: sparkwing.Failed, Err: err}
	}
	return runner.Result{Outcome: sparkwing.Success, Output: output}
}

// failedDaemonAcquire maps a failed daemon acquisition onto the node's
// terminal outcome: skip and fail policies mirror the store path's
// outcomes, a bounded queue wait that elapsed finalizes with the
// queue_timeout failure reason, and a cancelled run stays a cancellation.
func (r *InProcessRunner) failedDaemonAcquire(ctx, acquireCtx context.Context, req runner.Request, key string, queueTimeout time.Duration, err error) runner.Result {
	node := req.Node
	if errors.Is(context.Cause(acquireCtx), errNodeQueueTimeout) && ctx.Err() == nil {
		terr := fmt.Errorf("concurrency key %q: queued %s without a slot under OnLimit:Queue", key, queueTimeout)
		payload, _ := json.Marshal(map[string]any{
			"key":           key,
			"queue_timeout": queueTimeout.String(),
		})
		_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_queue_timeout", payload)
		_ = r.backends.State.FinishNodeWithReason(ctx, req.RunID, node.ID(),
			string(sparkwing.Failed), terr.Error(), nil, store.FailureQueueTimeout, nil)
		return runner.Result{Outcome: sparkwing.Failed, Err: terr}
	}
	if ctx.Err() != nil {
		r.markFailed(ctx, req.RunID, node.ID(), ctx.Err())
		return runner.Result{Outcome: sparkwing.Failed, Err: ctx.Err()}
	}
	var admErr *wingdclient.AdmissionError
	if errors.As(err, &admErr) {
		switch admErr.Policy {
		case wingwire.PolicySkip:
			return r.applySkippedConcurrent(ctx, req)
		case wingwire.PolicyFail:
			ferr := fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", key)
			r.markFailed(ctx, req.RunID, node.ID(), ferr)
			return runner.Result{Outcome: sparkwing.Failed, Err: ferr}
		}
	}
	werr := fmt.Errorf("concurrency acquire(%q): %w", key, err)
	r.markFailed(ctx, req.RunID, node.ID(), werr)
	return runner.Result{Outcome: sparkwing.Failed, Err: werr}
}
