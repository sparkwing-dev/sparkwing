package chaos

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// capacityEpsilon absorbs float rounding when comparing held cost to
// declared capacity; it is far below any real over-admission.
const capacityEpsilon = 1e-6

// checkLedgerTruth asserts the invariants that must hold on every
// [wingwire.QueueState] read: granted cost never exceeds capacity, holds
// are non-negative, holders and waiters are disjoint and duplicate-free,
// and waiter timings are sane. It returns one violation string per
// broken invariant; an empty slice means the snapshot is sound.
func checkLedgerTruth(qs wingwire.QueueState) []string {
	var v []string
	for _, r := range qs.Resources {
		if r.Held > r.Capacity+capacityEpsilon {
			v = append(v, fmt.Sprintf("over capacity on %q: held %g exceeds capacity %g", r.Key, r.Held, r.Capacity))
		}
		if r.Held < -capacityEpsilon {
			v = append(v, fmt.Sprintf("negative held on %q: %g", r.Key, r.Held))
		}
	}
	holders := map[string]int{}
	for _, h := range qs.Holders {
		holders[h.RunID]++
	}
	for id, n := range holders {
		if n > 1 {
			v = append(v, fmt.Sprintf("run %q holds %d leases, want 1", id, n))
		}
	}
	waiters := map[string]int{}
	for _, w := range qs.Waiters {
		waiters[w.RunID]++
		if w.WaitingMS < 0 {
			v = append(v, fmt.Sprintf("waiter %q has negative wait %d", w.RunID, w.WaitingMS))
		}
	}
	for id, n := range waiters {
		if n > 1 {
			v = append(v, fmt.Sprintf("run %q waits %d times, want 1", id, n))
		}
		if holders[id] > 0 {
			v = append(v, fmt.Sprintf("run %q both holds and waits", id))
		}
	}
	return v
}

// checkOSTruth cross-checks the daemon's holder set against the set of
// crashdummy processes the harness believes are alive. A holder the
// harness never spawned is a phantom -- always a violation. A holder whose
// process is confirmed dead is a leaked lease, but only when leakStable is
// true: right after a daemon kill the successor restores leases and holds
// them for the reattach grace window, so a dead client's restored lease
// legitimately lingers until grace expiry. Permanent leaks are caught by
// [checkConverged] regardless, so gating the transient check on daemon
// stability trades no real coverage for freedom from grace-window flakes.
func checkOSTruth(qs wingwire.QueueState, live, known map[string]bool, leakStable bool) []string {
	var v []string
	for _, h := range qs.Holders {
		if !known[h.RunID] {
			v = append(v, fmt.Sprintf("phantom holder %q: not a run the harness spawned", h.RunID))
			continue
		}
		if leakStable && !live[h.RunID] {
			v = append(v, fmt.Sprintf("leaked lease: %q holds admission but its process is gone", h.RunID))
		}
	}
	return v
}

// checkConverged asserts total quiescence: no holders, no waiters, no
// held capacity. It is evaluated only after the harness has stopped
// injecting faults and waited out the settle window.
func checkConverged(qs wingwire.QueueState) []string {
	var v []string
	if n := len(qs.Holders); n > 0 {
		v = append(v, fmt.Sprintf("did not converge: %d holders remain", n))
	}
	if n := len(qs.Waiters); n > 0 {
		v = append(v, fmt.Sprintf("did not converge: %d waiters remain", n))
	}
	for _, r := range qs.Resources {
		if r.Held > capacityEpsilon {
			v = append(v, fmt.Sprintf("did not converge: %q still holds %g", r.Key, r.Held))
		}
	}
	return v
}
