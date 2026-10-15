package nodehost

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type runEnd struct {
	status  string
	err     error
	skipped bool
}

// safety: the dispatcher's order: the store's groups, then the lease carrying the
// box- and run-scoped groups. The store's groups end with body; the lease
// outlives it until release.
func (a *Admission) hostRun(ctx context.Context, run *Run, cancel context.CancelCauseFunc, body func(context.Context) runEnd) (end runEnd, release func()) {
	release = func() {}
	claims, inStore := a.planGroups(run.Doc, run.ID)
	if len(inStore) > 0 && a.Slots == nil {
		return runEnd{status: "failed", err: fmt.Errorf("plan concurrency group %q: no coordination store", inStore[0].key)}, release
	}
	decided := false
	step := func(ctx context.Context) Result {
		decided = true
		var ok bool
		if release, end, ok = a.runLease(ctx, run, claims, cancel); ok {
			end = body(ctx)
		}
		switch {
		case end.status == "success" && !end.skipped:
			return Result{Outcome: sparkwing.Success}
		case end.skipped:
			return Result{Outcome: sparkwing.Skipped}
		}
		return Result{Outcome: sparkwing.Failed}
	}
	for _, g := range slices.Backward(inStore) {
		next := step
		step = func(ctx context.Context) Result { return a.underSlot(ctx, runHolder(run), g, next) }
	}
	res := step(ctx)
	if decided {
		return end, release
	}
	// safety: a store group refused the run before the lease or any node.
	switch res.Outcome {
	case sparkwing.Skipped:
		return runEnd{status: "success", skipped: true}, release
	case sparkwing.Superseded:
		return stoppedRun(ctx, &CancelError{Reason: "plan concurrency group: admission evicted; run `sparkwing queue` to inspect the queue"}), release
	}
	return stoppedRun(ctx, res.Err), release
}

// safety: a cancel cause outranks the error it caused, so an interrupt
// while queued reads as cancelled, as it does in the dispatcher.
func stoppedRun(ctx context.Context, err error) runEnd {
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	return runEnd{status: runStatus(err), err: err}
}

func runHolder(run *Run) slotHolder {
	return slotHolder{
		run: run, runID: run.ID, holder: run.ID + "/-", log: run.Logger,
		activity: func(d string) {
			if d != "" {
				run.Logger.Info("nodehost: run queued for a plan concurrency group", "run", run.ID, "detail", d)
			}
		},
	}
}

// safety: each list is in key order, so two runs never take one another's
// groups in opposite orders.
func (a *Admission) planGroups(doc nodeproto.PlanDoc, runID string) (claims []wingwire.SemaphoreClaim, inStore []group) {
	pcs := doc.Concurrency
	seen := map[string]bool{}
	for _, pc := range pcs {
		key := ConcurrencyKey(pc, runID, a.boxID())
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		local := pc.Scope == "run" || pc.Scope == "box"
		if a.Daemon != nil && local {
			claims = append(claims, wingwire.SemaphoreClaim{
				Name: key, Cost: pc.Cost, Capacity: pc.Capacity, Policy: wingwire.Policy(pc.OnLimit),
				QueueTimeoutMS: value(pc.QueueTimeoutMS), CancelTimeoutMS: value(pc.CancelTimeoutMS),
			})
			continue
		}
		inStore = append(inStore, group{
			key: key, policy: pc.OnLimit, capacity: pc.Capacity, cost: pc.Cost,
			queueTO:  time.Duration(value(pc.QueueTimeoutMS)) * time.Millisecond,
			cancelTO: time.Duration(value(pc.CancelTimeoutMS)) * time.Millisecond,
		})
	}
	slices.SortFunc(claims, func(x, y wingwire.SemaphoreClaim) int { return strings.Compare(x.Name, y.Name) })
	slices.SortFunc(inStore, func(x, y group) int { return strings.Compare(x.key, y.key) })
	return claims, inStore
}

// safety: semaphores-only unless the plan declares resources, as the
// dispatcher's run lease is.
func (a *Admission) runLease(ctx context.Context, run *Run, claims []wingwire.SemaphoreClaim, cancel context.CancelCauseFunc) (release func(), refused runEnd, ok bool) {
	release = func() {}
	if a.Daemon == nil {
		return release, runEnd{}, true
	}
	doc := run.Doc
	req := wingwire.AdmissionRequest{
		RunID: run.ID, Pipeline: run.Pipeline, Semaphores: claims,
		SemaphoresOnly: !hostAdmitted(doc), Priority: a.priority(doc), Class: a.class(doc),
	}
	if !req.SemaphoresOnly {
		req.Resources = wingwire.HostResources{Cores: value(doc.Resources.Cores), MemoryBytes: value(doc.Resources.MemoryBytes)}
	}
	acquireCtx := ctx
	if key, timeout := tightestQueueTimeout(claims); timeout > 0 {
		var stop context.CancelFunc
		acquireCtx, stop = context.WithTimeoutCause(ctx, timeout,
			fmt.Errorf("plan concurrency group %q: queued %s without a slot under OnLimit:Queue; run `sparkwing queue` to see who holds it", key, timeout))
		defer stop()
	}
	lease, err := a.Daemon.Acquire(acquireCtx, req, func(q wingwire.Queued) {
		run.Logger.Info("nodehost: run queued for local admission", "run", run.ID, "key", q.Key, "position", q.Position)
	})
	if err != nil {
		return release, refusedRun(ctx, acquireCtx, run, claims, err), false
	}
	run.leaseToken = lease.Token()
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		err := lease.Watch(func(ev wingwire.Evicted) {
			cancel(&CancelError{Reason: fmt.Sprintf("admission on %q lost under %s: superseded by run %s; run `sparkwing queue` to inspect the queue", ev.Key, ev.Policy, ev.SupersededBy)})
		}, func(c wingwire.Cancel) {
			cancel(&CancelError{Reason: cmp.Or(c.Reason, "cancelled via the admission daemon")})
		})
		if err != nil {
			// safety: as in the dispatcher, a lost watch leaves the run running
			// without eviction or daemon-side cancel.
			run.Logger.Warn("nodehost: run lease watch ended; the run continues unwatched", "run", run.ID, "err", err)
		}
	}()
	return func() {
		if err := lease.Release(); err != nil {
			run.Logger.Warn("nodehost: release run lease", "run", run.ID, "err", err)
		}
		<-watching
	}, runEnd{}, true
}

func refusedRun(ctx, acquireCtx context.Context, run *Run, claims []wingwire.SemaphoreClaim, err error) runEnd {
	var admErr *wingdclient.AdmissionError
	var cancelled *wingdclient.CancelledError
	switch {
	case ctx.Err() != nil:
		return stoppedRun(ctx, ctx.Err())
	case context.Cause(acquireCtx) != nil:
		return runEnd{status: "failed", err: context.Cause(acquireCtx)}
	case errors.As(err, &cancelled):
		return runEnd{status: "cancelled", err: &CancelError{Reason: cmp.Or(cancelled.Reason, "cancelled via the admission daemon")}}
	case errors.As(err, &admErr) && admErr.Policy == wingwire.PolicySkip:
		hostEvent(ctx, run, "", "plan_skipped_concurrent", nil)
		return runEnd{status: "success", skipped: true}
	case errors.As(err, &admErr) && admErr.Policy == wingwire.PolicyFail:
		hostEvent(ctx, run, "", "plan_failed_concurrent", nil)
		if !slices.ContainsFunc(claims, func(c wingwire.SemaphoreClaim) bool { return c.Name == admErr.Key }) {
			break
		}
		return runEnd{status: "failed", err: fmt.Errorf("plan concurrency group %q: slot full under OnLimit:Fail; run `sparkwing queue` to see who holds it", admErr.Key)}
	}
	return runEnd{status: "failed", err: fmt.Errorf("local admission: %w", err)}
}

func tightestQueueTimeout(claims []wingwire.SemaphoreClaim) (key string, timeout time.Duration) {
	for _, c := range claims {
		if c.QueueTimeoutMS <= 0 || (c.Policy != "" && c.Policy != wingwire.PolicyQueue) {
			continue
		}
		if d := time.Duration(c.QueueTimeoutMS) * time.Millisecond; timeout == 0 || d < timeout {
			key, timeout = c.Name, d
		}
	}
	return key, timeout
}
