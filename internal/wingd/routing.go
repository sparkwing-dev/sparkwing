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
	qlen := d.waiterCountLocked()
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
			c.role = roleHolder
			c.leaseID = ev.Lease
			c.members = []string{ev.RequestID}
			c.startAt = d.now()
			d.leaseCharge[ev.Lease] = c.resources
			out = append(out, delivery{c, &wingwire.Grant{
				RunID:      ev.RequestID,
				LeaseToken: lease.Token,
				Resources:  c.resources,
			}})
		case admission.EventQueued:
			if c := d.byRun[ev.RequestID]; c != nil {
				out = append(out, delivery{c, &wingwire.Queued{
					RunID:          ev.RequestID,
					Position:       ev.Position + 1,
					QueueLength:    qlen,
					BlockingReason: d.hostBlockingReasonLocked(c.resources),
				}})
			}
		case admission.EventEvicted:
			if c := d.byRun[ev.RequestID]; c != nil {
				out = append(out, delivery{c, &wingwire.Evicted{
					RunID:        ev.RequestID,
					Key:          ev.Key,
					SupersededBy: d.leaseRun[ev.SupersededBy],
					Policy:       wingwire.PolicyCancelOthers,
				}})
			}
		case admission.EventReleased:
			delete(d.leaseRun, ev.Lease)
			delete(d.leaseCharge, ev.Lease)
			delete(d.leaseMembers, ev.Lease)
		}
	}
	return out
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
