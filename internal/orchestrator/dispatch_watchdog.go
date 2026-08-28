package orchestrator

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type admissionWaitTracker struct {
	mu      sync.Mutex
	active  map[string]int
	changed chan struct{}
}

func newAdmissionWaitTracker() *admissionWaitTracker {
	return &admissionWaitTracker{active: make(map[string]int), changed: make(chan struct{}, 1)}
}

func (t *admissionWaitTracker) begin(participant string) {
	if t == nil || participant == "" {
		return
	}
	t.mu.Lock()
	t.active[participant]++
	t.mu.Unlock()
	t.signal()
}

func (t *admissionWaitTracker) end(participant string) {
	if t == nil || participant == "" {
		return
	}
	t.mu.Lock()
	if t.active[participant] <= 1 {
		delete(t.active, participant)
	} else {
		t.active[participant]--
	}
	t.mu.Unlock()
	t.signal()
}

func (t *admissionWaitTracker) signal() {
	if t == nil {
		return
	}
	select {
	case t.changed <- struct{}{}:
	default:
	}
}

func (t *admissionWaitTracker) covers(participants []string) bool {
	if t == nil || len(participants) == 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, participant := range participants {
		if t.active[participant] == 0 {
			return false
		}
	}
	return true
}

type admissionWaitTrackerKey struct{}

func withAdmissionWaitTracker(ctx context.Context, tracker *admissionWaitTracker) context.Context {
	return context.WithValue(ctx, admissionWaitTrackerKey{}, tracker)
}

func admissionWaitTrackerFromContext(ctx context.Context) *admissionWaitTracker {
	tracker, _ := ctx.Value(admissionWaitTrackerKey{}).(*admissionWaitTracker)
	return tracker
}

type admissionWaitParticipantKey struct{}

func withAdmissionWaitParticipant(ctx context.Context, participant string) context.Context {
	return context.WithValue(ctx, admissionWaitParticipantKey{}, participant)
}

func admissionWaitParticipantFromContext(ctx context.Context) string {
	participant, _ := ctx.Value(admissionWaitParticipantKey{}).(string)
	return participant
}

const DefaultDispatchWaitTimeout = 30 * time.Minute

const dispatchTimeoutDrainMargin = time.Minute

const dispatchStackDumpBytes = 1 << 20

const maxDuration = time.Duration(1<<63 - 1)

func defaultDispatchWaitTimeoutForPlan(plan *sparkwing.Plan) time.Duration {
	if plan == nil {
		return DefaultDispatchWaitTimeout
	}

	nodes := plan.Nodes()
	byID := make(map[string]*sparkwing.JobNode, len(nodes))
	for _, node := range nodes {
		byID[node.ID()] = node
	}
	memo := make(map[string]time.Duration, len(nodes))
	visiting := make(map[string]bool, len(nodes))
	var pathBudget func(*sparkwing.JobNode) time.Duration
	pathBudget = func(node *sparkwing.JobNode) time.Duration {
		if budget, ok := memo[node.ID()]; ok {
			return budget
		}
		if visiting[node.ID()] {
			return 0
		}
		visiting[node.ID()] = true
		var dependencyBudget time.Duration
		for _, dependencyID := range node.DepIDs() {
			if dependency := byID[dependencyID]; dependency != nil {
				dependencyBudget = max(dependencyBudget, pathBudget(dependency))
			}
		}
		delete(visiting, node.ID())
		budget := saturatingDurationAdd(dependencyBudget, nodeExecutionBudget(node))
		memo[node.ID()] = budget
		return budget
	}

	longest := DefaultDispatchWaitTimeout
	for _, node := range nodes {
		budget := pathBudget(node)
		if recovery := node.OnFailureNode(); recovery != nil {
			budget = saturatingDurationAdd(budget, nodeExecutionBudget(recovery))
		}
		longest = max(longest, saturatingDurationAdd(budget, dispatchTimeoutDrainMargin))
	}
	return longest
}

func nodeExecutionBudget(node *sparkwing.JobNode) time.Duration {
	timeout := node.TimeoutDuration()
	if timeout <= 0 {
		return 0
	}
	retry := node.RetryConfig()
	attempts := retry.Attempts + 1
	if attempts <= 0 || int64(attempts) > int64(maxDuration/timeout) {
		return maxDuration
	}
	budget := timeout * time.Duration(attempts)
	for attempt := 1; attempt <= retry.Attempts; attempt++ {
		budget = saturatingDurationAdd(budget, scaledBackoff(retry.Backoff, attempt))
		if budget == maxDuration {
			break
		}
	}
	return budget
}

func saturatingDurationAdd(a, b time.Duration) time.Duration {
	if b > 0 && a > maxDuration-b {
		return maxDuration
	}
	return a + b
}

type dispatchWaitResult int

const (
	dispatchWaitDone dispatchWaitResult = iota
	dispatchWaitTimedOut
)

func waitForDispatch(
	wg *sync.WaitGroup,
	timeout time.Duration,
	waits *admissionWaitTracker,
	activeParticipants func() []string,
) dispatchWaitResult {
	return waitForDispatchObserved(wg, timeout, waits, activeParticipants, nil)
}

func waitForDispatchObserved(
	wg *sync.WaitGroup,
	timeout time.Duration,
	waits *admissionWaitTracker,
	activeParticipants func() []string,
	observePause func(bool),
) dispatchWaitResult {
	if timeout <= 0 {
		wg.Wait()
		return dispatchWaitDone
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		paused := waits != nil && waits.covers(activeParticipants())
		if observePause != nil {
			observePause(paused)
		}
		if paused {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			select {
			case <-done:
				return dispatchWaitDone
			case <-waits.changed:
			}
			timer.Reset(timeout)
			continue
		}
		select {
		case <-done:
			return dispatchWaitDone
		case <-timer.C:
			if waits != nil && waits.covers(activeParticipants()) {
				timer.Reset(timeout)
				continue
			}
			return dispatchWaitTimedOut
		case <-waits.changed:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(timeout)
		}
	}
}

func stuckNodeIDs(plan *sparkwing.Plan, state *dispatchState) []string {
	var stuck []string
	for _, n := range watchdogKnownNodes(plan, state) {
		if _, ok := state.getOutcome(n.ID()); !ok {
			stuck = append(stuck, n.ID())
		}
	}
	return stuck
}

func (s *dispatchState) watchdogActiveNodeIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make([]string, 0, len(s.starts))
	for id := range s.starts {
		if _, done := s.outcomes[id]; !done {
			active = append(active, id)
		}
	}
	return active
}

func watchdogKnownNodes(plan *sparkwing.Plan, state *dispatchState) []*sparkwing.JobNode {
	known := plan.Nodes()
	seen := make(map[string]struct{}, len(known))
	for _, n := range known {
		seen[n.ID()] = struct{}{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for id, n := range state.scheduled {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		known = append(known, n)
	}
	return known
}

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

func dumpAllGoroutineStacks(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = dispatchStackDumpBytes
	}
	buf := make([]byte, maxBytes)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
