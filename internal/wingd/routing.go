package wingd

import (
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// routeLocked turns ledger events into frames addressed to connections.
// A grant or promotion whose owning connection is gone is released at
// once so its capacity is not stranded, which can cascade into further
// events; the loop drains them all. The caller holds d.mu.
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
			if c.finalizable {
				wait := int64(0)
				if !c.startAt.IsZero() {
					wait = now.Sub(c.startAt).Milliseconds()
				}
				d.events.record(now, admissionEvent{Kind: eventGrant, WaitMS: wait})
			}
			c.role = roleHolder
			c.leaseID = ev.Lease
			c.members = []string{ev.RequestID}
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
				out = append(out, delivery{c, &wingwire.Queued{
					RunID:          ev.RequestID,
					Position:       ev.Position + 1,
					QueueLength:    d.waiterCountLocked(),
					BlockingReason: d.hostBlockingReasonLocked(c.resources),
				}})
			}
		case admission.EventEvicted:
			d.events.record(d.now(), admissionEvent{Kind: eventEviction, Key: ev.Key})
			if c := d.byRun[ev.RequestID]; c != nil {
				out = append(out, delivery{c, &wingwire.Evicted{
					RunID:        ev.RequestID,
					Key:          ev.Key,
					SupersededBy: d.leaseRun[ev.SupersededBy],
					Policy:       wingwire.PolicyCancelOthers,
				}})
			}
		case admission.EventReleased:
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
			Position:       waiterPosition(snap.Waiters[:i], waiter) + 1,
			QueueLength:    qlen,
			BlockingReason: d.hostBlockingReasonLocked(c.resources),
		}})
	}
	return out
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

// cancelWaiterLocked removes a queued run whose connection died. The
// ledger has no waiter-removal primitive, so the queue is rebuilt from a
// snapshot: restore the holders alone, then re-submit every surviving
// waiter in arrival order. Per the ledger's FIFO guarantees this
// reproduces the exact post-cancellation state -- including promoting a
// lighter waiter that the dead one was blocking -- and the re-submits'
// events carry those promotions and position changes back out. The caller
// holds d.mu.
func (d *Daemon) cancelWaiterLocked(runID string) []admission.Event {
	snap := d.ledger.Snapshot()
	base := snap
	base.Waiters = nil
	lg, err := admission.Restore(base, nil)
	if err != nil {
		panic(fmt.Sprintf("wingd: rebuild ledger without holders failed: %v", err))
	}
	var events []admission.Event
	for _, w := range snap.Waiters {
		if w.RequestID == runID {
			continue
		}
		_, evs, err := lg.Submit(requestFromWaiter(w))
		if err != nil {
			panic(fmt.Sprintf("wingd: re-submit waiter %q during rebuild: %v", w.RequestID, err))
		}
		events = append(events, evs...)
	}
	d.ledger = lg
	return events
}

func requestFromWaiter(w admission.WaiterState) admission.Request {
	req := admission.Request{
		ID:          w.RequestID,
		Cores:       float64(w.MilliCores) / 1000.0,
		MemoryBytes: w.MemoryBytes,
	}
	for _, c := range w.Claims {
		req.Semaphores = append(req.Semaphores, admission.SemaphoreClaim{
			Key:      c.Key,
			Capacity: c.Capacity,
			Cost:     c.Cost,
			Policy:   c.Policy,
		})
	}
	return req
}

func (d *Daemon) waiterCountLocked() int {
	return len(d.ledger.Snapshot().Waiters)
}

// soleRunUnderLoadLocked reports whether the liveness floor -- not ordinary
// headroom -- is what let this run in: a finalizable host run whose charge
// exceeds the currently grantable cores, which can only be granted when the
// box is otherwise idle of sparkwing work. That is the signal the client
// narrates as "admitted as sole run; additional runs will queue". The caller
// holds d.mu.
func (d *Daemon) soleRunUnderLoadLocked(c *conn) bool {
	return c.finalizable && d.headroomInit &&
		c.resources.Cores > 0 && c.resources.Cores > d.appliedCores
}
