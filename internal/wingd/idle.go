package wingd

import (
	"context"
	"fmt"
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
				d.shutdownWithReason("idle")
				return
			}
		}
	}
}

func (d *Daemon) idleElapsed() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stay || hasLocalActivity(d.localActivity) || len(d.byRun) > 0 {
		return 0
	}
	if len(d.reattachWait) > 0 {
		return 0
	}
	snap := d.ledger.Snapshot()
	if len(snap.Leases) > 0 || len(snap.Waiters) > 0 {
		return 0
	}
	return d.now().Sub(d.lastActivity)
}

func hasLocalActivity(activity LocalActivity) bool {
	return activity.Runs != 0 || activity.QueuedTriggers != 0 || activity.HeldLeases != 0 || activity.DashboardConnections != 0
}

// SetLocalActivity supplies the controller's live activity independently of API probes.
func (d *Daemon) SetLocalActivity(activity LocalActivity) error {
	if activity.Runs < 0 || activity.QueuedTriggers < 0 || activity.HeldLeases < 0 || activity.DashboardConnections < 0 {
		return fmt.Errorf("wingd: local activity counts must be nonnegative")
	}
	activity.IdleSince = time.Time{}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.localActivity != activity {
		d.localActivity = activity
		d.touchLocked()
	}
	return nil
}

// SetStay pins the controller until it is drained or stay mode is removed.
func (d *Daemon) SetStay(stay bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stay != stay {
		d.stay = stay
		d.touchLocked()
	}
}
