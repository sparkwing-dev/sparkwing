package orchestrator

import (
	"log/slog"
	"runtime"
	"sync"
	"time"

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
func waitForDispatch(wg *sync.WaitGroup, timeout time.Duration) dispatchWaitResult {
	if timeout <= 0 {
		wg.Wait()
		return dispatchWaitDone
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return dispatchWaitDone
	case <-time.After(timeout):
		return dispatchWaitTimedOut
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
