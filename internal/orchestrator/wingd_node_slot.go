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

var errNodeQueueTimeout = errors.New("queue timeout")

func (r *NodeExecutor) runNodeUnderDaemonSem(ctx context.Context, req runner.Request, la *LocalAdmission, group *sparkwing.ConcurrencyGroup) runner.Result {
	node := req.Node
	_, _, hostAdmitted := localAdmissionFromContext(ctx)
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
	if hostAdmitted && limit.OnLimit == sparkwing.Queue && limit.QueueTimeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeoutCause(ctx, limit.QueueTimeout, errNodeQueueTimeout)
		defer cancel()
	}
	var cancelCombined context.CancelCauseFunc
	var combinedSemTimer *time.Timer
	if !hostAdmitted && limit.OnLimit == sparkwing.Queue && limit.QueueTimeout > 0 {
		acquireCtx, cancelCombined = context.WithCancelCause(ctx)
		defer cancelCombined(nil)
		defer func() {
			if combinedSemTimer != nil {
				combinedSemTimer.Stop()
			}
		}()
	}

	waited := false
	lastDetail := ""
	releasedWorker := false
	releaseWorker := func() {
		if releasedWorker || req.ReleaseWorkerSlot == nil {
			return
		}
		req.ReleaseWorkerSlot()
		releasedWorker = true
	}
	onQueued := func(q wingwire.Queued) {
		if !waited {
			waited = true
			releaseWorker()
			payload, _ := json.Marshal(map[string]any{
				"key":          key,
				"kind":         "queued",
				"position":     q.Position,
				"queue_length": q.QueueLength,
			})
			_ = r.backends.State.AppendEvent(ctx, req.RunID, node.ID(), "concurrency_wait", payload)
		}
		if cancelCombined != nil {
			if q.Key == key && combinedSemTimer == nil {
				combinedSemTimer = time.AfterFunc(limit.QueueTimeout, func() {
					cancelCombined(errNodeQueueTimeout)
				})
			}
			if q.Key != key && combinedSemTimer != nil {
				combinedSemTimer.Stop()
				combinedSemTimer = nil
			}
		}
		if detail := fmt.Sprintf("queued in %s: %d ahead", key, max(0, q.Position-1)); detail != lastDetail {
			lastDetail = detail
			_ = r.backends.State.UpdateNodeActivity(ctx, req.RunID, node.ID(), detail)
			r.emitConcWaitLog(ctx, req, detail)
		}
	}

	if !hostAdmitted {
		releaseWorker()
	}

	var lease *wingdclient.Lease
	var err error
	priority := localAdmissionPriorityFromContext(ctx)
	if hostAdmitted {
		lease, err = la.acquireNodeSlot(acquireCtx, req.RunID, node.ID(), claim, priority, onQueued)
	} else {
		lease, err = la.acquireNodeHostSlot(acquireCtx, r.backends, req.Pipeline, req.RunID, node.ID(), node, claim,
			priority, onQueued)
	}
	if err != nil {
		return r.failedDaemonAcquire(ctx, acquireCtx, req, claim, limit.QueueTimeout, err)
	}

	if releasedWorker || waited {
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

	runCtx := execCtx
	if !hostAdmitted {
		childToken := localAdmissionChildTokenFromContext(ctx)
		if childToken == "" {
			childToken = lease.Token
		}
		runCtx = withLocalAdmission(execCtx, la, lease.Token, childToken, leaseCarriesHost(lease), localAdmissionPriorityFromContext(execCtx))
	}
	output, err := r.executeNodeWithAdmission(runCtx, req)
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

func (r *NodeExecutor) failedDaemonAcquire(ctx, acquireCtx context.Context, req runner.Request, claim wingwire.SemaphoreClaim, queueTimeout time.Duration, err error) runner.Result {
	node := req.Node
	key := claim.Name
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
			ferr := nodeAdmissionFailure(claim, admErr)
			r.markFailed(ctx, req.RunID, node.ID(), ferr)
			return runner.Result{Outcome: sparkwing.Failed, Err: ferr}
		}
	}
	werr := fmt.Errorf("concurrency acquire(%q): %w", key, err)
	r.markFailed(ctx, req.RunID, node.ID(), werr)
	return runner.Result{Outcome: sparkwing.Failed, Err: werr}
}
