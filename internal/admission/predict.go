package admission

import "math"

// Predict replays a snapshot with the live scheduler and projected milliseconds
// until holders finish and queued work completes. Missing durations are unknown.
// It never mutates the snapshot or the ledger that produced it.
func Predict(snap Snapshot, durations map[string]float64) (map[string]float64, float64, error) {
	starts := make(map[string]float64, len(snap.Waiters))
	for _, w := range snap.Waiters {
		starts[w.RequestID] = math.Inf(1)
	}
	l, err := Restore(snap, nil)
	if err != nil {
		return starts, math.Inf(1), err
	}
	finish := map[LeaseID]float64{}
	duration := func(id string) float64 {
		if ms := durations[id]; ms > 0 {
			return ms
		}
		return math.Inf(1)
	}
	for _, le := range l.leases {
		holding := le.milliCores > 0 || le.memory > 0
		for _, c := range le.claims {
			for _, h := range l.sems[c.key].holds {
				holding = holding || h.lease == le.id && !h.superseded && h.cost > 0
			}
		}
		if holding {
			finish[le.id] = duration(le.requestID)
		}
	}
	now := 0.0
	for len(l.waiters) > 0 {
		next := math.Inf(1)
		for _, at := range finish {
			next = min(next, at)
		}
		if math.IsInf(next, 1) {
			return starts, next, nil
		}
		now = next
		// safety: equal completion times free capacity together, before backfill consumes it.
		for _, id := range l.sortedLeaseIDs() {
			if at, ok := finish[id]; ok && at == now {
				l.free(l.leases[id])
				delete(finish, id)
			}
		}
		for _, event := range l.promote() {
			if event.Kind == EventPromoted {
				starts[event.RequestID] = now
				finish[event.Lease] = now + duration(event.RequestID)
			}
		}
	}
	for _, at := range finish {
		now = max(now, at)
	}
	return starts, now, nil
}
