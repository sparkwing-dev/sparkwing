package nodehost

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Admission admits each node the way the in-process dispatcher does. The
// local daemon leases the node's cores and memory and runs its box- and
// run-scoped concurrency group; the coordination store runs every other
// group, and every group when there is no daemon.
type Admission struct {
	// Daemon grants local leases. Nil takes no host leases.
	Daemon Admitter
	// Slots is the coordination store. A node whose group it would run fails
	// when it is nil.
	Slots ConcurrencySlots
	// Priority, when set, replaces the plan's priority, as --sw-priority does.
	Priority *int
	// Class is the admission class the trigger implies; the plan's own class
	// wins over it, and normal applies when both are empty.
	Class string
	// BoxID names this machine in box-scoped group keys; nil uses the hostname.
	BoxID func() string
}

// Admitter asks the local admission daemon for a lease. A request that is
// not semaphores-only and names no resources is charged from the node's
// measured profile, so the implementation fills in that cost, the repository,
// the origin and the process to sample. It calls onQueued on the caller's
// goroutine.
type Admitter interface {
	Acquire(ctx context.Context, req wingwire.AdmissionRequest, onQueued func(wingwire.Queued)) (Lease, error)
}

// Lease is a granted daemon lease. Token names it to the daemon; a node lease
// carries its run lease's token as proof that it belongs to the run. Watch is the lease connection's only
// reader for the lease's whole life: it answers the daemon's liveness probes,
// calls onEvicted when a cancel_others arrival takes the lease's semaphore and
// onCancel when the daemon cancels the holder, and returns once Release ends
// the lease, or with [wingdclient.ErrReattachRejected] when a restarted daemon
// no longer has it.
type Lease interface {
	Token() string
	Release() error
	Watch(onEvicted func(wingwire.Evicted), onCancel func(wingwire.Cancel)) error
}

// ConcurrencySlots is the coordination store's slot surface, the part of the
// dispatcher's concurrency backend a concurrency group uses.
type ConcurrencySlots interface {
	AcquireSlot(ctx context.Context, req store.AcquireSlotRequest) (store.AcquireSlotResponse, error)
	ObserveSlot(ctx context.Context, key, holderID string) (*store.ConcurrencyHolder, error)
	HeartbeatSlot(ctx context.Context, key, holderID string, lease time.Duration) (expires time.Time, superseded bool, err error)
	ReleaseSlot(ctx context.Context, key, holderID, outcome, outputRef, cacheKeyHash string, ttl time.Duration) error
	ResolveWaiter(ctx context.Context, key, runID, nodeID, cacheKeyHash, leaderRunID, leaderNodeID string, bypassRead bool) (store.WaiterResolution, error)
	ForceReleaseSuperseded(ctx context.Context, key string) ([]store.ConcurrencyHolder, error)
	CancelWaiter(ctx context.Context, key, runID, nodeID string) (bool, error)
}

var waiterPoll = 100 * time.Millisecond

// safety: mirrors orchestrator.DefaultStoreWedgeBudget, so a store outage
// fails a queued node no sooner than the dispatcher fails it.
const storeOutageBudget = 5 * time.Minute

// hack: the outage clock is swappable so a test can cross the budget without waiting minutes.
var outageClock = time.Now

var errQueueTimeout = errors.New("queue timeout")

func admissionLayer(cfg *Config, next Executor) Executor {
	a := cfg.Admission
	if a == nil {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result { return a.admit(ctx, n, next) }
}

type group struct {
	key, policy       string
	capacity, cost    int
	queueTO, cancelTO time.Duration
	// safety: set only by the memo layer, so a concurrency group's release records no memo entry.
	memoHash string
	memoTTL  time.Duration
}

func (a *Admission) admit(ctx context.Context, n *NodeRun, next Executor) Result {
	m := n.Plan.Modifiers
	if m == nil || m.ConcGroup == "" {
		return a.withHost(ctx, n, next)
	}
	g := group{
		key:      nodeproto.ConcurrencyGroupKey(m.ConcGroup, m.ConcScope, n.Run.ID, a.boxID),
		policy:   m.ConcOnLimit,
		capacity: m.ConcCapacity, cost: m.ConcCost,
		queueTO:  time.Duration(m.ConcQueueTimeoutMS) * time.Millisecond,
		cancelTO: time.Duration(m.ConcCancelTimeoutMS) * time.Millisecond,
	}
	if a.Daemon != nil && (m.ConcScope == string(sparkwing.ScopeBox) || m.ConcScope == string(sparkwing.ScopeRun)) {
		return a.underDaemon(ctx, n, g, next)
	}
	if a.Slots == nil {
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency group %q: no coordination store", m.ConcGroup)}
	}
	return a.underSlot(ctx, a.nodeHolder(ctx, n), g, func(ctx context.Context) Result { return a.withHost(ctx, n, next) })
}

func (a *Admission) boxID() string {
	if a.BoxID != nil {
		return a.BoxID()
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}

// safety: a run whose own lease carries the plan's resources already holds
// the host, so its nodes take no host lease of their own, as in the dispatcher.
func hostAdmitted(doc nodeproto.PlanDocOld) bool {
	return doc.Resources != nil && (doc.Resources.Cores > 0 || doc.Resources.MemoryBytes > 0)
}

// safety: ids match the dispatcher's, so the queue view and the daemon's
// owner checks read a hosted node's lease the same way.
func (a *Admission) request(n *NodeRun, claim *wingwire.SemaphoreClaim) wingwire.AdmissionRequest {
	doc := n.Run.Doc
	req := wingwire.AdmissionRequest{
		OwnerRunID: n.Run.ID, OwnerLeaseToken: n.Run.leaseToken,
		DisplayRunID: n.Run.ID + "/" + n.Plan.ID, Pipeline: doc.Pipeline,
		SubLease: true, Priority: a.priority(doc),
	}
	if claim != nil {
		req.Semaphores = []wingwire.SemaphoreClaim{*claim}
	}
	if hostAdmitted(doc) {
		req.RunID, req.SemaphoresOnly = wingwire.NodeSemaphoreParticipant(n.Run.ID, n.Plan.ID), true
		return req
	}
	req.RunID = wingwire.NodeHostParticipant(n.Run.ID, n.Plan.ID)
	req.Class = a.class(doc)
	if m := n.Plan.Modifiers; m != nil {
		req.Resources = wingwire.HostResources{Cores: m.ResCores, MemoryBytes: m.ResMemoryBytes}
	}
	return req
}

func (a *Admission) priority(doc nodeproto.PlanDocOld) int {
	if a.Priority != nil {
		return *a.Priority
	}
	return doc.Priority
}

func (a *Admission) class(doc nodeproto.PlanDocOld) string {
	return cmp.Or(doc.AdmissionClass, a.Class, string(sparkwing.AdmissionNormal))
}

func (a *Admission) withHost(ctx context.Context, n *NodeRun, next Executor) Result {
	if a.Daemon == nil || hostAdmitted(n.Run.Doc) {
		return next(ctx, n)
	}
	onQueued, waited := a.queuedReporter(ctx, n, nil)
	lease, err := a.Daemon.Acquire(ctx, a.request(n, nil), onQueued)
	if err != nil {
		return admissionFailed(ctx, fmt.Errorf("local admission: %w", err))
	}
	if waited() {
		a.activity(ctx, n, "")
	}
	return a.runLeased(ctx, n, lease, next)
}

func (a *Admission) underDaemon(ctx context.Context, n *NodeRun, g group, next Executor) Result {
	claim := wingwire.SemaphoreClaim{
		Name: g.key, Cost: g.cost, Capacity: g.capacity,
		Policy: wingwire.Policy(g.policy), CancelTimeoutMS: g.cancelTO.Milliseconds(),
	}
	acquireCtx, cancelAcquire := context.WithCancelCause(ctx)
	defer cancelAcquire(nil)
	// safety: the queue timeout counts only time spent waiting on the group,
	// not on host capacity, so it restarts whenever the wait moves off the key.
	var mu sync.Mutex
	var timer *time.Timer
	queuedOnce := false
	defer func() {
		mu.Lock()
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
	}()
	onQueued := func(q wingwire.Queued) {
		if !queuedOnce {
			queuedOnce = true
			hostEventJSON(ctx, n.Run, n.Plan.ID, "concurrency_wait", map[string]any{
				"key": g.key, "kind": "queued", "position": q.Position, "queue_length": q.QueueLength,
			})
		}
		if (g.policy != store.OnLimitQueue && g.policy != "") || g.queueTO <= 0 {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case q.Key == g.key && timer == nil:
			timer = time.AfterFunc(g.queueTO, func() { cancelAcquire(errQueueTimeout) })
		case q.Key != g.key && timer != nil:
			timer.Stop()
			timer = nil
		}
	}
	reporter, waited := a.queuedReporter(ctx, n, onQueued)
	lease, err := a.Daemon.Acquire(acquireCtx, a.request(n, &claim), reporter)
	if err != nil {
		var admErr *wingdclient.AdmissionError
		switch {
		case errors.Is(context.Cause(acquireCtx), errQueueTimeout) && ctx.Err() == nil:
			hostEventJSON(ctx, n.Run, n.Plan.ID, "concurrency_queue_timeout", map[string]any{"key": g.key, "queue_timeout": g.queueTO.String()})
			return queueTimedOut(g)
		case ctx.Err() != nil:
			return admissionFailed(ctx, ctx.Err())
		case errors.As(err, &admErr) && admErr.Policy == wingwire.PolicySkip:
			return Result{Outcome: sparkwing.SkippedConcurrent}
		case errors.As(err, &admErr) && admErr.Policy == wingwire.PolicyFail:
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency key %q: slot full under OnLimit:Fail", g.key)}
		}
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency acquire(%q): %w", g.key, err)}
	}
	if waited() {
		hostEvent(ctx, n.Run, n.Plan.ID, "concurrency_promoted", nil)
		a.activity(ctx, n, "")
	}
	return a.runLeased(ctx, n, lease, next)
}

// safety: the watcher answers wingd's liveness probes for the lease's whole
// life, or wingd reclaims the capacity while the node still runs.
func (a *Admission) runLeased(ctx context.Context, n *NodeRun, lease Lease, next Executor) Result {
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	var ended atomic.Pointer[Result]
	end := func(r Result) {
		ended.CompareAndSwap(nil, &r)
		cancelExec()
	}
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		err := lease.Watch(func(ev wingwire.Evicted) {
			end(Result{Outcome: sparkwing.Superseded, Err: fmt.Errorf("concurrency key %q: superseded by run %s under %s", ev.Key, ev.SupersededBy, ev.Policy)})
		}, func(c wingwire.Cancel) {
			reason := c.Reason
			if reason == "" {
				reason = "cancelled via the admission daemon"
			}
			end(Result{Outcome: sparkwing.Cancelled, Err: errors.New(reason)})
		})
		switch {
		case errors.Is(err, wingdclient.ErrReattachRejected):
			end(Result{Outcome: sparkwing.Superseded, Err: fmt.Errorf("local admission lease lost: %w", err)})
		case err != nil:
			// safety: an unreachable daemon admits no one else, so the node keeps
			// running unwatched, as a dispatcher run does.
			n.Run.Logger.Warn("nodehost: admission lease watch ended; the node continues unwatched", "node", n.Plan.ID, "err", err)
		}
	}()
	res := next(execCtx, n)
	a.release(n, lease)
	<-watching
	if r := ended.Load(); r != nil {
		return *r
	}
	return res
}

func (a *Admission) queuedReporter(ctx context.Context, n *NodeRun, also func(wingwire.Queued)) (onQueued func(wingwire.Queued), waited func() bool) {
	last := ""
	return func(q wingwire.Queued) {
		if also != nil {
			also(q)
		}
		if d := fmt.Sprintf("queued in %s: %d ahead", q.Key, max(0, q.Position-1)); d != last {
			last = d
			a.activity(ctx, n, d)
		}
	}, func() bool { return last != "" }
}

func (a *Admission) activity(ctx context.Context, n *NodeRun, detail string) {
	if err := n.Run.State.UpdateNodeActivity(context.WithoutCancel(ctx), n.Run.ID, n.Plan.ID, detail); err != nil {
		n.Run.Logger.Warn("nodehost: node activity", "node", n.Plan.ID, "err", err)
	}
}

func (a *Admission) release(n *NodeRun, lease Lease) {
	if err := lease.Release(); err != nil {
		n.Run.Logger.Warn("nodehost: release admission lease", "node", n.Plan.ID, "err", err)
	}
}

// safety: a node stopped by its run's cancel reports a plain failure, which
// the scheduler records as cancelled.
func admissionFailed(ctx context.Context, err error) Result {
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return Result{Outcome: sparkwing.Failed, Err: err}
}

func queueTimedOut(g group) Result {
	return Result{
		Outcome: sparkwing.Failed, Reason: store.FailureQueueTimeout,
		Err: fmt.Errorf("concurrency key %q: queued %s without a slot under OnLimit:Queue", g.key, g.queueTO),
	}
}

// safety: the store knows a whole run's hold by an empty node and the holder
// <run>/-, as the dispatcher writes it.
type slotHolder struct {
	run                   *Run
	runID, nodeID, holder string
	activity              func(detail string)
	log                   *slog.Logger
}

func (a *Admission) nodeHolder(ctx context.Context, n *NodeRun) slotHolder {
	return slotHolder{
		run: n.Run, runID: n.Run.ID, nodeID: n.Plan.ID, holder: n.Run.ID + "/" + n.Plan.ID,
		activity: func(d string) { a.activity(ctx, n, d) }, log: n.Run.Logger,
	}
}

func (a *Admission) underSlot(ctx context.Context, h slotHolder, g group, run func(context.Context) Result) Result {
	resp, err := a.Slots.AcquireSlot(ctx, store.AcquireSlotRequest{
		Key: g.key, HolderID: h.holder, RunID: h.runID, NodeID: h.nodeID,
		Capacity: g.capacity, Cost: g.cost, Policy: g.policy, CancelTimeout: g.cancelTO,
	})
	if err != nil {
		return admissionFailed(ctx, fmt.Errorf("concurrency acquire(%q): %w", g.key, err))
	}
	if resp.DriftNote != "" {
		h.concEvent(ctx, g, "concurrency_drift", map[string]any{
			"key": g.key, "previous_capacity": resp.PreviousCapacity, "new_capacity": g.capacity, "note": resp.DriftNote,
		})
	}
	switch resp.Kind {
	case store.AcquireGranted:
		return a.holdSlot(ctx, h, g, h.holder, run)
	case store.AcquireSkipped:
		if h.nodeID == "" {
			hostEvent(ctx, h.run, "", "plan_skipped_concurrent", nil)
		}
		return Result{Outcome: sparkwing.SkippedConcurrent}
	case store.AcquireFailed:
		if h.nodeID == "" {
			hostEvent(ctx, h.run, "", "plan_failed_concurrent", nil)
		}
		return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency key %q slot full under OnLimit:Fail", g.key)}
	case store.AcquireQueued, store.AcquireCancellingOthers:
		return a.waitSlot(ctx, h, g, resp, run)
	}
	return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency acquire(%q) returned unexpected kind %q", g.key, resp.Kind)}
}

func (a *Admission) waitSlot(ctx context.Context, h slotHolder, g group, initial store.AcquireSlotResponse, run func(context.Context) Result) Result {
	h.concEvent(ctx, g, "concurrency_wait", map[string]any{
		"key": g.key, "kind": string(initial.Kind), "position": initial.Position,
		"queue_length": initial.QueueLength, "holders": concHolders(initial.Holders),
		"leader_run_id": initial.LeaderRunID, "leader_node_id": initial.LeaderNodeID,
	})
	if initial.Kind == store.AcquireQueued {
		h.activity(fmt.Sprintf("queued in %s: %d ahead", g.key, initial.Position))
	}
	if initial.Kind == store.AcquireCancellingOthers && g.cancelTO > 0 {
		timer := time.AfterFunc(g.cancelTO, func() {
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, err := a.Slots.ForceReleaseSuperseded(fctx, g.key); err != nil {
				h.log.Warn("nodehost: force-release after cancel timeout", "key", g.key, "err", err)
			}
		})
		defer timer.Stop()
	}
	var deadline time.Time
	if g.queueTO > 0 && initial.Kind == store.AcquireQueued {
		deadline = time.Now().Add(g.queueTO)
	}
	cancelWaiter := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := a.Slots.CancelWaiter(cctx, g.key, h.runID, h.nodeID); err != nil {
			h.log.Warn("nodehost: cancel concurrency waiter; the reaper sweeps it", "key", g.key, "err", err)
		}
	}
	tick := time.NewTicker(waiterPoll)
	defer tick.Stop()
	var failingSince time.Time
	for {
		select {
		case <-ctx.Done():
			cancelWaiter()
			return admissionFailed(ctx, ctx.Err())
		case <-tick.C:
		}
		res, err := a.Slots.ResolveWaiter(ctx, g.key, h.runID, h.nodeID, "", "", "", false)
		if err != nil {
			// safety: a store failing for the dispatcher's whole wedge budget, or with
			// a lock-protocol error retrying cannot clear, fails the node.
			if failingSince.IsZero() {
				failingSince = outageClock()
			}
			if store.IsProtocolErr(err) || outageClock().Sub(failingSince) >= storeOutageBudget {
				cancelWaiter()
				return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency key %q: resolve waiter: %w", g.key, err)}
			}
			continue
		}
		failingSince = time.Time{}
		switch res.Status {
		case store.WaiterStillWaiting:
			if !deadline.IsZero() && time.Now().After(deadline) {
				cancelWaiter()
				h.concEvent(ctx, g, "concurrency_queue_timeout", map[string]any{"key": g.key, "queue_timeout": g.queueTO.String()})
				return queueTimedOut(g)
			}
		case store.WaiterPromoted:
			h.concEvent(ctx, g, "concurrency_promoted", nil)
			h.activity("")
			return a.holdSlot(ctx, h, g, res.HolderID, run)
		case store.WaiterCancelled:
			h.concEvent(ctx, g, "concurrency_cancelled", nil)
			return Result{Outcome: sparkwing.Superseded, Err: fmt.Errorf("concurrency key %q: waiter was cancelled or superseded", g.key)}
		default:
			cancelWaiter()
			return Result{Outcome: sparkwing.Failed, Err: fmt.Errorf("concurrency key %q: unexpected waiter status %q", g.key, res.Status)}
		}
	}
}

// safety: a holder superseded or out of contact past its lease stops its
// node or run, so two holders of one slot never keep running together. The
// cancel cause marks a run so stopped as cancelled rather than failed.
func (a *Admission) holdSlot(ctx context.Context, h slotHolder, g group, holder string, run func(context.Context) Result) Result {
	execCtx, cancelExec := context.WithCancelCause(ctx)
	defer cancelExec(nil)
	lostErr := &lease.CancelError{Reason: fmt.Sprintf("concurrency key %q: holder superseded by newer arrival", g.key)}
	var lost atomic.Bool
	done := make(chan struct{})
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		a.watchSlot(execCtx, g, holder, done, func() { lost.Store(true); cancelExec(lostErr) })
	}()
	res := run(execCtx)
	close(done)
	<-watching
	if lost.Load() {
		res = Result{Outcome: sparkwing.Superseded, Err: lostErr}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.Slots.ReleaseSlot(rctx, g.key, holder, slotOutcome(res.Outcome), holder, g.memoHash, g.memoTTL); err != nil {
		h.log.Warn("nodehost: concurrency release failed; the reaper sweeps it", "key", g.key, "err", err)
	}
	return res
}

func (a *Admission) watchSlot(ctx context.Context, g group, holder string, done <-chan struct{}, lose func()) {
	beat := time.NewTicker(store.ConcurrencyHeartbeatInterval(g.policy))
	defer beat.Stop()
	var observe <-chan time.Time
	if g.policy != store.OnLimitCancelOthers {
		t := time.NewTicker(store.DefaultConcurrencyHeartbeatInterval)
		defer t.Stop()
		observe = t.C
	}
	lastOK := time.Now()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-observe:
			octx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.DefaultConcurrencyHeartbeatTimeout)
			h, err := a.Slots.ObserveSlot(octx, g.key, holder)
			cancel()
			if errors.Is(err, store.ErrNotFound) || (err == nil && (h == nil || h.Superseded)) {
				lose()
				return
			}
		case <-beat.C:
			hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), store.ConcurrencyHeartbeatTimeout(g.policy))
			_, superseded, err := a.Slots.HeartbeatSlot(hctx, g.key, holder, store.DefaultConcurrencyLease)
			cancel()
			switch {
			case errors.Is(err, store.ErrLockHeld), err == nil && superseded:
				lose()
				return
			case err == nil:
				lastOK = time.Now()
			case time.Since(lastOK) >= store.DefaultConcurrencyLease:
				// safety: past a whole lease without contact another holder may own the slot.
				lose()
				return
			}
		}
	}
}

// safety: a plan group's event is the run's, scoped as the dispatcher writes
// it, so a reader tells a run's wait from a node's. A node's promoted and
// cancelled events carry no payload, as the dispatcher's do.
func (h slotHolder) concEvent(ctx context.Context, g group, kind string, fields map[string]any) {
	if h.nodeID == "" {
		if fields == nil {
			fields = map[string]any{}
		}
		fields["key"], fields["scope"], fields["resource"] = g.key, "plan", "plan_admission"
	}
	hostEventJSON(ctx, h.run, h.nodeID, kind, fields)
}

// safety: capped as the dispatcher caps a plan wait's holders, so one event stays small.
func concHolders(holders []store.ConcurrencyHolder) []map[string]any {
	out := make([]map[string]any, 0, min(len(holders), 8))
	for _, h := range holders[:min(len(holders), 8)] {
		out = append(out, map[string]any{"run_id": h.RunID, "node_id": h.NodeID, "holder_id": h.HolderID, "lease_expires_at": h.LeaseExpiresAt})
	}
	return out
}

func slotOutcome(o sparkwing.Outcome) string {
	switch o {
	case sparkwing.Success, sparkwing.Cached:
		return "success"
	case sparkwing.Skipped, sparkwing.SkippedConcurrent:
		return "skipped"
	case sparkwing.Superseded:
		return "superseded"
	}
	return "failed"
}
