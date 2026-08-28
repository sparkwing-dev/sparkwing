package wingd

import (
	"context"
	"time"
)

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
