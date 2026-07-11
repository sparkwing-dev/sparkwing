package wingd

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// stallRecoveryCommand is the single non-destructive verb a queue view
// advertises for a wedged holder. It cancels one run by id and never
// touches shared host state.
func stallRecoveryCommand(runID string) string {
	return fmt.Sprintf("sparkwing runs cancel --run %s", runID)
}

// buildQueueStateLocked renders the current admission picture for the
// read-only queue view: capacity rows with held amounts, holders with
// elapsed time and cost, and waiters in admission order. The caller holds
// d.mu.
func (d *Daemon) buildQueueStateLocked() wingwire.QueueState {
	snap := d.ledger.Snapshot()
	var qs wingwire.QueueState

	var usedMilli int64
	var usedMem uint64
	semHeld := map[string]int{}
	for _, ls := range snap.Leases {
		usedMilli += ls.MilliCores
		usedMem += ls.MemoryBytes
	}
	for _, ss := range snap.Semaphores {
		held := 0
		for _, h := range ss.Holds {
			if !h.Superseded {
				held += h.Cost
			}
		}
		semHeld[ss.Key] = held
	}

	qs.Resources = append(qs.Resources,
		wingwire.ResourceState{
			Key:      "cores",
			Capacity: float64(snap.TotalMilliCores) / 1000.0,
			Held:     float64(usedMilli) / 1000.0,
		},
		wingwire.ResourceState{
			Key:      "memory",
			Capacity: float64(snap.TotalMemoryBytes),
			Held:     float64(usedMem),
		},
	)
	for _, ss := range snap.Semaphores {
		qs.Resources = append(qs.Resources, wingwire.ResourceState{
			Key:      ss.Key,
			Capacity: float64(effectiveCapacity(ss)),
			Held:     float64(semHeld[ss.Key]),
		})
	}

	now := d.now()
	for _, ls := range snap.Leases {
		h := wingwire.Holder{
			RunID: ls.RequestID,
			Resources: wingwire.HostResources{
				Cores:       float64(ls.MilliCores) / 1000.0,
				MemoryBytes: int64(ls.MemoryBytes),
			},
			Semaphores: claimKeys(ls.Claims),
		}
		if c := d.byRun[ls.RequestID]; c != nil {
			h.Pipeline = c.pipeline
			if !c.startAt.IsZero() {
				h.ElapsedMS = now.Sub(c.startAt).Milliseconds()
			}
			if c.stalled {
				h.Stalled = true
				h.Recovery = stallRecoveryCommand(ls.RequestID)
			}
		}
		qs.Holders = append(qs.Holders, h)
	}

	remaining := map[string]float64{}
	for _, r := range qs.Resources {
		remaining[r.Key] = r.Capacity - r.Held
	}
	for i, w := range snap.Waiters {
		waiter := wingwire.Waiter{
			RunID:    w.RequestID,
			Position: i + 1,
			Resources: wingwire.HostResources{
				Cores:       float64(w.MilliCores) / 1000.0,
				MemoryBytes: int64(w.MemoryBytes),
			},
			Semaphores: claimKeys(w.Claims),
			WaitingOn:  waitingOn(w, remaining),
		}
		if c := d.byRun[w.RequestID]; c != nil {
			waiter.Pipeline = c.pipeline
			if !c.startAt.IsZero() {
				waiter.WaitingMS = now.Sub(c.startAt).Milliseconds()
			}
		}
		qs.Waiters = append(qs.Waiters, waiter)
	}
	return qs
}

// waitingOn names the resources a waiter cannot fit into right now: host
// dimensions and full semaphore keys whose remaining room is smaller than
// what the waiter draws. An empty result means the waiter is blocked only
// by arrival order behind a heavier request ahead of it.
func waitingOn(w admission.WaiterState, remaining map[string]float64) []string {
	var keys []string
	if cores := float64(w.MilliCores) / 1000.0; cores > 0 && remaining["cores"] < cores {
		keys = append(keys, "cores")
	}
	if mem := float64(w.MemoryBytes); mem > 0 && remaining["memory"] < mem {
		keys = append(keys, "memory")
	}
	for _, c := range w.Claims {
		if room, ok := remaining[c.Key]; ok && room < float64(c.Cost) {
			keys = append(keys, c.Key)
		}
	}
	return keys
}

// effectiveCapacity is the smallest capacity any live hold declares for a
// semaphore, matching the ledger's most-restrictive-wins rule.
func effectiveCapacity(ss admission.SemaphoreState) int {
	eff := 0
	for _, h := range ss.Holds {
		if h.Superseded {
			continue
		}
		if eff == 0 || h.Capacity < eff {
			eff = h.Capacity
		}
	}
	if eff == 0 {
		return ss.LastCapacity
	}
	return eff
}

func claimKeys(claims []admission.ClaimState) []string {
	if len(claims) == 0 {
		return nil
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Key)
	}
	return out
}
