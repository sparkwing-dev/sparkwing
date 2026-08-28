package chaos

import (
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func daemonGraceStable(qs wingwire.QueueState, grace, settle time.Duration) bool {
	if qs.DaemonUptimeMS == 0 {
		return true
	}
	return time.Duration(qs.DaemonUptimeMS)*time.Millisecond > grace+settle
}

const capacityEpsilon = 1e-6

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

func checkLivenessTruth(qs wingwire.QueueState) []string {
	if len(qs.Holders) > 0 || len(qs.Waiters) == 0 {
		return nil
	}
	for _, r := range qs.Resources {
		if r.Held > capacityEpsilon {
			return nil
		}
	}
	return []string{fmt.Sprintf(
		"liveness floor violated: %d waiter(s) queued while no run holds admission", len(qs.Waiters))}
}

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
