package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// errNoLocalAdmission is what a tool-slot acquire reports when the run
// carries no admission daemon, so [sparkwing.ToolSlot] can narrate the
// fallback instead of proceeding as if it held a budget.
var errNoLocalAdmission = errors.New("run has no local admission daemon")

// toolSlotProvider builds the [sparkwing.ToolSlotProvider] installed on
// a node's context. It acquires the group as a semaphores-only sub-lease
// on the daemon, which is what the wire protocol already reserves for
// "short-lived semaphore acquisitions made from inside an already-admitted
// run", so a tool lock needs no new resource kind.
//
// The lease is a fresh connection held for the tool's runtime only, not
// the node's, because a gate step that serializes a linter must release
// the budget the moment the linter exits rather than pinning it through
// the rest of a long job.
func (r *InProcessRunner) toolSlotProvider(runID, nodeID string, delegate sparkwing.Logger) sparkwing.ToolSlotProvider {
	return func(ctx context.Context, group *sparkwing.ConcurrencyGroup, cost int) (func(), error) {
		la, _, _ := localAdmissionFromContext(ctx)
		if la == nil {
			return nil, errNoLocalAdmission
		}
		limit := group.Limit()
		key := scopedGroupKey(group, runID)
		claim := wingwire.SemaphoreClaim{
			Name:            key,
			Cost:            cost,
			Capacity:        limit.Capacity,
			Policy:          wingwire.Policy(limit.OnLimit),
			CancelTimeoutMS: limit.CancelTimeout.Milliseconds(),
		}

		acquireCtx := ctx
		if limit.QueueTimeout > 0 {
			var cancel context.CancelFunc
			acquireCtx, cancel = context.WithTimeoutCause(ctx, limit.QueueTimeout, errNodeQueueTimeout)
			defer cancel()
		}

		start := time.Now()
		lastDetail := ""
		announced := false
		onQueued := func(q wingwire.Queued) {
			if !announced {
				announced = true
				payload, _ := json.Marshal(map[string]any{
					"key":          key,
					"kind":         "queued",
					"scope":        "tool",
					"position":     q.Position,
					"queue_length": q.QueueLength,
				})
				_ = r.backends.State.AppendEvent(ctx, runID, nodeID, "concurrency_wait", payload)
			}
			detail := fmt.Sprintf("queued for %s: %d ahead of %d", key, max(0, q.Position-1), q.QueueLength)
			if q.BlockingReason != "" {
				detail += " (" + q.BlockingReason + ")"
			}
			if detail == lastDetail {
				return
			}
			lastDetail = detail
			_ = r.backends.State.UpdateNodeActivity(ctx, runID, nodeID, detail)
			r.emitToolSlotLog(runID, nodeID, delegate, detail)
		}

		resumeProgressTimeout := pauseProgressTimeout(ctx)
		lease, err := la.acquireNodeSlot(acquireCtx, runID, nodeID, claim,
			localAdmissionPriorityFromContext(ctx), onQueued)
		resumeProgressTimeout()
		if err != nil {
			if errors.Is(context.Cause(acquireCtx), errNodeQueueTimeout) && ctx.Err() == nil {
				return nil, fmt.Errorf("queued %s for %q without a slot", limit.QueueTimeout, key)
			}
			return nil, err
		}
		if announced {
			_ = r.backends.State.AppendEvent(ctx, runID, nodeID, "concurrency_promoted", nil)
			_ = r.backends.State.UpdateNodeActivity(ctx, runID, nodeID, "")
			r.emitToolSlotLog(runID, nodeID, delegate,
				fmt.Sprintf("admitted to %s after %s", key, time.Since(start).Round(time.Second)))
		}

		var once sync.Once
		return func() { once.Do(func() { _ = lease.Release() }) }, nil
	}
}

// emitToolSlotLog mirrors a tool-slot wait line into the node log so the
// position shows up where an operator is already looking, matching how a
// queued node reports itself.
func (r *InProcessRunner) emitToolSlotLog(runID, nodeID string, delegate sparkwing.Logger, detail string) {
	nlog, err := r.backends.Logs.OpenNodeLog(runID, nodeID, delegate)
	if err != nil {
		return
	}
	nlog.Emit(sparkwing.LogRecord{TS: time.Now(), Level: "info", Event: "concurrency_wait", Msg: detail})
	_ = nlog.Close()
}
