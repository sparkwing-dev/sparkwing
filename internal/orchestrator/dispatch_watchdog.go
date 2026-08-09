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

// dispatchWaitResult reports how waitForDispatch returned.
type dispatchWaitResult int

const (
	dispatchWaitDone     dispatchWaitResult = iota // all per-node goroutines finished
	dispatchWaitTimedOut                           // timeout elapsed first
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
func waitForDispatch(
	wg *sync.WaitGroup,
	timeout time.Duration,
	waits *admissionWaitTracker,
	activeParticipants func() []string,
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
		if waits != nil && waits.covers(activeParticipants()) {
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

// stuckNodeIDs lists known nodes with no recorded outcome at the
// moment the watchdog fired -- the dispatcher's view of "which
// goroutines never reported back." The known set is the static plan
// plus runtime-scheduled dynamic and recovery nodes, so a wedged
// fan-out member is named too. A node that emitted node_end in the
// envelope but whose state-store write didn't commit (the SQLite
// snapshot-conflict failure mode) shows up here as well, which is
// exactly the signal an on-call wants: log says done, dispatcher
// disagrees, here are the candidates.
func stuckNodeIDs(plan *sparkwing.Plan, state *dispatchState) []string {
	var stuck []string
	for _, n := range watchdogKnownNodes(plan, state) {
		if _, ok := state.getOutcome(n.ID()); !ok {
			stuck = append(stuck, n.ID())
		}
	}
	return stuck
}

// watchdogActiveNodeIDs excludes nodes that have not started because they are
// waiting on dependencies. Admission can pause the watchdog only when every
// started, unfinished node is itself in the admission queue.
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

// watchdogKnownNodes unions the static plan with the nodes the
// dispatcher scheduled at runtime (dynamic fan-out members, recovery
// runners) that never appear in Plan.Nodes(), deduped by ID.
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
