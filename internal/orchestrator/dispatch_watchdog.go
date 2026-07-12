package orchestrator

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// DefaultDispatchWaitTimeout bounds how long the dispatcher's
// post-DAG drain (state.wg.Wait) may block before the run is declared
// wedged. Picked to be generous enough that long-tail nodes don't hit
// it during normal operation -- node-level timeouts, controller
// reapers, and OS-level backpressure all act first -- while still
// turning an unbounded hang into a fail-fast within the same shift.
const DefaultDispatchWaitTimeout = 30 * time.Minute

// dispatchStackDumpBytes caps the captured goroutine dump so a
// pathological hang in a process with thousands of goroutines can't
// produce a multi-gigabyte envelope file.
const dispatchStackDumpBytes = 1 << 20 // 1 MiB

const dispatchWatchdogEventPageSize = 500

// dispatchWaitResult reports how waitForDispatch returned.
type dispatchWaitResult int

const (
	dispatchWaitDone     dispatchWaitResult = iota // all per-node goroutines finished
	dispatchWaitTimedOut                           // timeout elapsed first
	dispatchWaitAborted                            // dispatch context was cancelled before drain
)

// waitForDispatch blocks until wg drains or timeout elapses. A
// non-positive timeout means wait indefinitely -- the historical
// behavior, preserved as an explicit opt-out for operators who'd
// rather hang than fail-fast.
//
// On timeout the caller owns the fail-fast bookkeeping (event
// emission, slot release via deferred unwind). The leaked goroutines
// themselves are NOT killed; Go has no safe primitive for that, so
// they outlive the returning dispatcher and die with the process.
// Returning early is the entire point: a hung Wait holds the run's
// concurrency-namespace slot indefinitely and locks the rest of the
// fleet behind a process that will never make progress.
func waitForDispatch(ctx context.Context, wg *sync.WaitGroup, timeout time.Duration, canContinue func(context.Context, time.Time, time.Time) bool) dispatchWaitResult {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	if timeout <= 0 {
		select {
		case <-done:
			return dispatchWaitDone
		case <-ctx.Done():
			return dispatchWaitAborted
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	windowStartedAt := time.Now()
	for {
		select {
		case <-done:
			return dispatchWaitDone
		case <-ctx.Done():
			return dispatchWaitAborted
		case <-timer.C:
			if canContinue != nil && canContinue(ctx, windowStartedAt, time.Now()) {
				windowStartedAt = time.Now()
				timer.Reset(timeout)
				continue
			}
			return dispatchWaitTimedOut
		}
	}
}

// stuckNodeIDs lists plan nodes with no recorded outcome at the
// moment the watchdog fired -- the dispatcher's view of "which
// goroutines never reported back." A node that emitted node_end in
// the envelope but whose state-store write didn't commit (the SQLite
// snapshot-conflict failure mode) shows up here too, which is
// exactly the signal an on-call wants: log says done, dispatcher
// disagrees, here are the candidates.
func stuckNodeIDs(plan *sparkwing.Plan, state *dispatchState) []string {
	var stuck []string
	for _, n := range plan.Nodes() {
		if _, ok := state.getOutcome(n.ID()); !ok {
			stuck = append(stuck, n.ID())
		}
	}
	return stuck
}

type dispatchContinuation struct {
	Continue       bool
	Reason         string
	UnresolvedNode []string
}

func unresolvedNodesBlockedByAdmission(ctx context.Context, stateBackend StateBackend, runID string, plan *sparkwing.Plan, state *dispatchState, progressSince, evaluateAt time.Time) (dispatchContinuation, error) {
	unresolved := make(map[string]*sparkwing.JobNode)
	events, err := dispatchWatchdogEvents(ctx, stateBackend, runID)
	if err != nil {
		return dispatchContinuation{}, err
	}
	for _, node := range watchdogKnownNodes(plan, state) {
		if _, ok := state.getOutcome(node.ID()); !ok {
			unresolved[node.ID()] = node
		}
	}
	if len(unresolved) == 0 {
		return dispatchContinuation{}, nil
	}
	unresolvedIDs := sortedJobNodeIDs(unresolved)
	memo := make(map[string]bool, len(unresolved))
	visiting := make(map[string]bool, len(unresolved))
	progress := false
	admissionWait := false
	for nodeID := range unresolved {
		if nodeDispatchProgressSince(events, nodeID, progressSince) {
			progress = true
			continue
		}
		if nodeBlockedByAdmission(ctx, stateBackend, runID, plan, events, unresolved, memo, visiting, nodeID, evaluateAt) {
			admissionWait = true
			continue
		}
		return dispatchContinuation{}, nil
	}
	switch {
	case progress && admissionWait:
		return dispatchContinuation{Continue: true, Reason: "dispatch_progress_and_admission_wait", UnresolvedNode: unresolvedIDs}, nil
	case progress:
		return dispatchContinuation{Continue: true, Reason: "dispatch_progress", UnresolvedNode: unresolvedIDs}, nil
	case admissionWait:
		return dispatchContinuation{Continue: true, Reason: "admission_wait", UnresolvedNode: unresolvedIDs}, nil
	default:
		return dispatchContinuation{}, nil
	}
}

func watchdogKnownNodes(plan *sparkwing.Plan, state *dispatchState) []*sparkwing.JobNode {
	nodesByID := map[string]*sparkwing.JobNode{}
	for _, node := range plan.Nodes() {
		nodesByID[node.ID()] = node
		if recovery := node.OnFailureNode(); recovery != nil {
			nodesByID[recovery.ID()] = recovery
		}
	}
	if state != nil {
		for _, node := range state.scheduledJobNodes() {
			nodesByID[node.ID()] = node
		}
	}
	ids := make([]string, 0, len(nodesByID))
	for id := range nodesByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	nodes := make([]*sparkwing.JobNode, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, nodesByID[id])
	}
	return nodes
}

func sortedJobNodeIDs(nodes map[string]*sparkwing.JobNode) []string {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func nodeBlockedByAdmission(ctx context.Context, stateBackend StateBackend, runID string, plan *sparkwing.Plan, events []store.Event, unresolved map[string]*sparkwing.JobNode, memo, visiting map[string]bool, nodeID string, since time.Time) bool {
	if blocked, ok := memo[nodeID]; ok {
		return blocked
	}
	if visiting[nodeID] {
		return false
	}
	node := unresolved[nodeID]
	if node == nil {
		return false
	}
	if nodeWaitingForAdmission(events, node, since) {
		memo[nodeID] = true
		return true
	}
	visiting[nodeID] = true
	defer delete(visiting, nodeID)
	for _, depID := range watchdogDependencyIDs(plan, node) {
		if _, unresolvedDep := unresolved[depID]; unresolvedDep && nodeBlockedByAdmission(ctx, stateBackend, runID, plan, events, unresolved, memo, visiting, depID, since) {
			memo[nodeID] = true
			return true
		}
	}
	memo[nodeID] = false
	return false
}

func watchdogDependencyIDs(plan *sparkwing.Plan, node *sparkwing.JobNode) []string {
	ids := append([]string(nil), node.DepIDs()...)
	for _, group := range node.NeedsGroups() {
		select {
		case <-group.Ready():
			for _, member := range group.Members() {
				ids = append(ids, member.ID())
			}
		default:
			if plan != nil {
				ids = append(ids, plan.GroupSourceIDs(node.ID())...)
			}
		}
	}
	return ids
}

func nodeWaitingForAdmission(events []store.Event, node *sparkwing.JobNode, at time.Time) bool {
	waiting := false
	nodeID := node.ID()
	for _, event := range events {
		if event.NodeID != nodeID {
			continue
		}
		switch event.Kind {
		case "concurrency_wait":
			waiting = boundedQueueWait(event, node, at)
		case "concurrency_queue_timeout", "concurrency_promoted", "node_succeeded", "node_failed", "node_cancelled", "node_skipped":
			waiting = false
		}
	}
	return waiting
}

func boundedQueueWait(event store.Event, node *sparkwing.JobNode, at time.Time) bool {
	var payload struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return false
	}
	group := node.ConcurrencyGroupRef()
	if group == nil {
		return false
	}
	limit := group.Limit()
	onLimit := limit.OnLimit
	if onLimit == "" {
		onLimit = sparkwing.Queue
	}
	return payload.Kind == string(store.AcquireQueued) &&
		onLimit == sparkwing.Queue &&
		limit.QueueTimeout > 0 &&
		at.Before(event.TS.Add(limit.QueueTimeout))
}

func dispatchWatchdogEvents(ctx context.Context, stateBackend StateBackend, runID string) ([]store.Event, error) {
	var events []store.Event
	var afterSeq int64
	for {
		page, err := stateBackend.ListEventsAfter(ctx, runID, afterSeq, dispatchWatchdogEventPageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, page...)
		if len(page) < dispatchWatchdogEventPageSize {
			return events, nil
		}
		afterSeq = page[len(page)-1].Seq
	}
}

func nodeDispatchProgressSince(events []store.Event, nodeID string, since time.Time) bool {
	for _, event := range events {
		if event.NodeID != nodeID || !event.TS.After(since) {
			continue
		}
		switch event.Kind {
		case "concurrency_promoted", "node_started":
			return true
		}
	}
	return false
}

// parseDispatchWaitTimeout reads SPARKWING_DISPATCH_WAIT_TIMEOUT into
// a time.Duration with sensible fallbacks:
//
//   - empty / unparseable: zero (caller substitutes the default).
//   - "0" or "off" or "disable": negative sentinel, which
//     waitForDispatch treats as "wait indefinitely."
//   - otherwise: time.ParseDuration shape (e.g. "30m", "45s", "2h").
//
// Unparseable values log a warning and fall through to the default so
// a typo doesn't silently disable the watchdog.
func parseDispatchWaitTimeout(raw string) time.Duration {
	switch raw {
	case "":
		return 0
	case "0", "off", "disable", "disabled":
		return -1
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("SPARKWING_DISPATCH_WAIT_TIMEOUT: unparseable; using default",
			"value", raw, "err", err)
		return 0
	}
	return d
}

// dumpAllGoroutineStacks returns every live goroutine's stack as a
// single string, capped at maxBytes. The cap keeps the watchdog's
// envelope payload bounded regardless of process state.
func dumpAllGoroutineStacks(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = dispatchStackDumpBytes
	}
	buf := make([]byte, maxBytes)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
