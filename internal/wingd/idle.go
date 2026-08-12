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
// currently busy (any working connection, lease, or waiter). Health-probe
// connections do not count as busy: they observe the daemon on behalf of
// its supervisor, and a daemon held open by its own watchdog could never
// idle out.
func (d *Daemon) idleElapsed() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	for c := range d.conns {
		if !c.healthProbe {
			return 0
		}
	}
	if len(d.reattachWait) > 0 || len(d.guards) > 0 {
		return 0
	}
	snap := d.ledger.Snapshot()
	if len(snap.Leases) > 0 || len(snap.Waiters) > 0 {
		return 0
	}
	return d.now().Sub(d.lastActivity)
}
