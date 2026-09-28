package wingd

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func (d *Daemon) routeLocked(events []admission.Event) []delivery {
	var out []delivery
	queueChanged := false
	// safety: the ledger emits a grant's eviction events before the grant
	// itself, so the lease-to-run map must be seeded up front for an
	// Evicted frame to name its superseder.
	for _, ev := range events {
		if ev.Kind == admission.EventGranted || ev.Kind == admission.EventPromoted {
			d.leaseRun[ev.Lease] = ev.RequestID
		}
	}
	queue := append([]admission.Event(nil), events...)
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]
		switch ev.Kind {
		case admission.EventGranted, admission.EventPromoted:
			if ev.Kind == admission.EventPromoted {
				queueChanged = true
			}
			d.leaseRun[ev.Lease] = ev.RequestID
			c := d.byRun[ev.RequestID]
			if c == nil {
				more, err := d.ledger.Release(ev.Lease, ev.RequestID)
				if err == nil {
					queue = append(queue, more...)
				}
				continue
			}
			lease, _ := d.ledger.LeaseByID(ev.Lease)
			now := d.now()
			wait := int64(0)
			if !c.startAt.IsZero() {
				wait = now.Sub(c.startAt).Milliseconds()
			}
			usedCores, usedMem := d.usedLocked()
			freeCores := max(0, d.appliedCores-usedCores)
			freeMem := uint64(0)
			if d.appliedMem > usedMem {
				freeMem = d.appliedMem - usedMem
			}
			grantData := map[string]any{"backfill": ev.BackfillCount > 0, "headroom_before_cores": freeCores + c.resources.Cores, "headroom_after_cores": freeCores, "headroom_before_memory": freeMem + uint64(max(0, c.resources.MemoryBytes)), "headroom_after_memory": freeMem, "external_cores": d.externalCores, "external_memory": d.externalMem, "lease_id": ev.Lease}
			if c.finalizable {
				d.recordWindow(now, admissionEvent{Kind: eventGrant, WaitMS: wait}, c, grantData)
			} else {
				grantData["wait_ms"] = wait
				d.recordJournal("grant", c, grantData)
			}
			c.role = roleHolder
			c.leaseID = ev.Lease
			c.members = []string{ev.RequestID}
			d.leaseMembers[ev.Lease] = append([]string(nil), c.members...)
			c.startAt = now
			c.holdSampledMS = 0
			c.holdSaturatedMS = 0
			c.contended = false
			c.contentionReason = ""
			d.leaseCharge[ev.Lease] = c.resources
			if d.cfg.Budget.Enforcing() && c.finalizable && c.pid > 0 {
				go d.enforceHolderProcess(c.pid, ev.RequestID)
			}
			soleUnderLoad := d.soleRunUnderLoadLocked(c)
			grant := &wingwire.Grant{
				RunID:      ev.RequestID,
				LeaseToken: lease.Token,
				Resources:  c.resources,
			}
			if soleUnderLoad {
				grant.SoleRunUnderLoad = true
				grant.ExternalCores = d.externalCores
			}
			out = append(out, delivery{c, grant})
		case admission.EventQueued:
			if c := d.byRun[ev.RequestID]; c != nil {
				snap := d.ledger.Snapshot()
				queued := &wingwire.Queued{
					RunID:          ev.RequestID,
					Key:            blockingSemaphoreKeyForRun(snap, ev.RequestID),
					Position:       ev.Position + 1,
					QueueLength:    d.waiterCountLocked(),
					BlockingReason: d.hostBlockingReasonLocked(c),
				}
				resource := queueBlockerResource(queued.Key, queued.BlockingReason)
				d.recordJournal("queued", c, map[string]any{"position": queued.Position, "key": queued.Key, "resource": resource, "blocking_reason": queued.BlockingReason, "blocker": queueBlocker(snap, resource, ev.RequestID)})
				out = append(out, delivery{c, queued})
			}
		case admission.EventBackfilled:
			owner := d.byRun[ev.RequestID]
			if owner == nil {
				owner = &conn{runID: ev.RequestID}
			}
			d.recordWindow(d.now(), admissionEvent{Kind: eventBackfill, BackfillCount: ev.BackfillCount}, owner, map[string]any{"bypassed_by": ev.BypassedBy})
		case admission.EventEvicted:
			owner := d.byRun[ev.RequestID]
			if owner == nil {
				owner = &conn{runID: ev.RequestID}
			}
			d.recordWindow(d.now(), admissionEvent{Kind: eventEviction, Key: ev.Key}, owner, map[string]any{"reason": ev.Key, "superseded_by": d.leaseRun[ev.SupersededBy]})
			d.recordJournal("superseded", owner, map[string]any{"reason": ev.Key, "by_run": d.leaseRun[ev.SupersededBy]})
			if c := d.byRun[ev.RequestID]; c != nil {
				out = append(out, delivery{c, &wingwire.Evicted{
					RunID:        ev.RequestID,
					Key:          ev.Key,
					SupersededBy: d.leaseRun[ev.SupersededBy],
					Policy:       wingwire.PolicyCancelOthers,
				}})
			}
		case admission.EventReprioritized:
			queueChanged = true
		case admission.EventReleased:
			d.recordJournal("release", &conn{runID: ev.RequestID}, map[string]any{"lease_id": ev.Lease})
			queueChanged = true
			delete(d.leaseRun, ev.Lease)
			delete(d.leaseCharge, ev.Lease)
			delete(d.leaseMembers, ev.Lease)
		}
	}
	if queueChanged {
		out = append(out, d.waiterDeliveriesLocked()...)
	}
	return out
}

func queueBlockerResource(key, reason string) string {
	if key != "" {
		return "semaphore:" + key
	}
	if strings.Contains(reason, "cores") {
		return "cores"
	}
	if strings.Contains(reason, "memory") {
		return "memory"
	}
	return ""
}

func queueBlocker(snap admission.Snapshot, resource, runID string) string {
	for i, waiter := range snap.Waiters {
		if waiter.RequestID == runID {
			for j := i - 1; j >= 0; j-- {
				if _, ok := waiterResources(snap.Waiters[j])[resource]; ok {
					return snap.Waiters[j].RequestID
				}
			}
			break
		}
	}
	if strings.HasPrefix(resource, "semaphore:") {
		key := strings.TrimPrefix(resource, "semaphore:")
		for _, sem := range snap.Semaphores {
			if sem.Key != key {
				continue
			}
			for _, hold := range sem.Holds {
				if hold.Superseded {
					continue
				}
				for _, lease := range snap.Leases {
					if lease.ID == hold.Lease {
						return lease.RequestID
					}
				}
			}
		}
		return ""
	}
	for _, lease := range snap.Leases {
		switch resource {
		case "cores":
			if lease.MilliCores > 0 {
				return lease.RequestID
			}
		case "memory":
			if lease.MemoryBytes > 0 {
				return lease.RequestID
			}
		}
	}
	return ""
}

func (d *Daemon) waiterDeliveriesLocked() []delivery {
	snap := d.ledger.Snapshot()
	qlen := len(snap.Waiters)
	out := make([]delivery, 0, qlen)
	for i, waiter := range snap.Waiters {
		c := d.byRun[waiter.RequestID]
		if c == nil {
			continue
		}
		out = append(out, delivery{c, &wingwire.Queued{
			RunID:          waiter.RequestID,
			Key:            blockingSemaphoreKey(snap, waiter),
			Position:       waiterPosition(snap.Waiters[:i], waiter) + 1,
			QueueLength:    qlen,
			BlockingReason: d.hostBlockingReasonLocked(c),
		}})
	}
	return out
}

func (d *Daemon) queuedDeliveryLockedFromSnapshot(c *conn, snap admission.Snapshot, runID string) *delivery {
	qlen := len(snap.Waiters)
	for i, waiter := range snap.Waiters {
		if waiter.RequestID != runID {
			continue
		}
		return &delivery{c, &wingwire.Queued{
			RunID:          runID,
			Key:            blockingSemaphoreKey(snap, waiter),
			Position:       waiterPosition(snap.Waiters[:i], waiter) + 1,
			QueueLength:    qlen,
			BlockingReason: d.hostBlockingReasonLocked(c),
		}}
	}
	return nil
}

func blockingSemaphoreKeyForRun(snap admission.Snapshot, runID string) string {
	for _, waiter := range snap.Waiters {
		if waiter.RequestID == runID {
			return blockingSemaphoreKey(snap, waiter)
		}
	}
	return ""
}

func blockingSemaphoreKey(snap admission.Snapshot, waiter admission.WaiterState) string {
	remaining := semaphoreRemaining(snap)
	for _, claim := range waiter.Claims {
		if claim.Policy == admission.PolicyCancelOthers {
			continue
		}
		left, ok := remaining[claim.Key]
		if ok && left < claim.Cost {
			return claim.Key
		}
	}
	return ""
}

func semaphoreRemaining(snap admission.Snapshot) map[string]int {
	remaining := make(map[string]int, len(snap.Semaphores))
	for _, sem := range snap.Semaphores {
		used := 0
		for _, hold := range sem.Holds {
			if !hold.Superseded {
				used += hold.Cost
			}
		}
		remaining[sem.Key] = effectiveCapacity(sem) - used
	}
	return remaining
}

func waiterPosition(earlier []admission.WaiterState, waiter admission.WaiterState) int {
	mine := waiterResources(waiter)
	n := 0
	for _, prev := range earlier {
		if waitersOverlap(prev, mine) {
			n++
		}
	}
	return n
}

func waitersOverlap(waiter admission.WaiterState, resources map[string]struct{}) bool {
	for r := range waiterResources(waiter) {
		if _, ok := resources[r]; ok {
			return true
		}
	}
	return false
}

func waiterResources(waiter admission.WaiterState) map[string]struct{} {
	resources := map[string]struct{}{}
	if waiter.MilliCores > 0 {
		resources["cores"] = struct{}{}
	}
	if waiter.MemoryBytes > 0 {
		resources["memory"] = struct{}{}
	}
	for _, claim := range waiter.Claims {
		if claim.Policy == admission.PolicyCancelOthers {
			continue
		}
		resources["semaphore:"+claim.Key] = struct{}{}
	}
	return resources
}

func (d *Daemon) cancelWaiterLocked(runID string) []admission.Event {
	return d.ledger.CancelWaiter(runID)
}

func requestFromWaiter(w admission.WaiterState) admission.Request {
	req := admission.Request{
		ID:          w.RequestID,
		OwnerID:     w.OwnerID,
		Priority:    w.Priority,
		Cores:       float64(w.MilliCores) / 1000.0,
		SoftCores:   w.SoftCores,
		StrictCores: w.StrictCores,
		MemoryBytes: w.MemoryBytes,
	}
	for _, c := range w.Claims {
		req.Semaphores = append(req.Semaphores, admission.SemaphoreClaim(c))
	}
	return req
}

func (d *Daemon) waiterCountLocked() int {
	return len(d.ledger.Snapshot().Waiters)
}

func (d *Daemon) soleRunUnderLoadLocked(c *conn) bool {
	return c.finalizable && d.headroomInit &&
		c.resources.Cores > 0 && c.resources.Cores > d.appliedCores
}
