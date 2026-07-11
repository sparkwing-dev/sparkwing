package wingd

import (
	"context"
	"time"
)

// idleLoop stops the daemon once it has had no leases, no waiters, and no
// connections for a full idle window.
func (d *Daemon) idleLoop(ctx context.Context) {
	idle := d.cfg.idleTimeout()
	tick := idle / 4
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-t.C:
			if d.idleElapsed() >= idle {
				d.cfg.logf("idle for %s, exiting", idle)
				d.shutdown()
				return
			}
		}
	}
}

// idleElapsed returns how long the daemon has been idle, or zero if it is
// currently busy (any connection, lease, or waiter).
func (d *Daemon) idleElapsed() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.conns) > 0 || len(d.reattachWait) > 0 {
		return 0
	}
	snap := d.ledger.Snapshot()
	if len(snap.Leases) > 0 || len(snap.Waiters) > 0 {
		return 0
	}
	return d.now().Sub(d.lastActivity)
}
