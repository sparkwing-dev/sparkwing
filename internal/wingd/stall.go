package wingd

import (
	"context"
	"time"
)

// stallLoop samples holder process CPU on a slow cadence and marks
// holders that stay idle while runs queue behind them. It runs only for
// its side effect on holder state; the flag is surfaced through
// [wingwire.QueueState] and never acts on the process.
func (d *Daemon) stallLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.stallInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-t.C:
			d.stallTick()
		}
	}
}

// stallTick refreshes every holder's stall verdict. It samples process
// CPU only while runs are queued -- an empty queue clears any latched
// verdict and skips sampling entirely, so the cost is paid only under
// contention. A holder below the CPU threshold for the whole stall
// window latches stalled; any reading at or above it clears the verdict.
func (d *Daemon) stallTick() {
	window := d.cfg.stallWindow()
	threshold := d.cfg.stallCPUFraction()
	now := d.now()

	d.mu.Lock()
	hasWaiters := d.waiterCountLocked() > 0
	if !hasWaiters {
		for c := range d.conns {
			c.stalled = false
			c.lowSince = time.Time{}
		}
		d.mu.Unlock()
		return
	}
	var holders []*conn
	for c := range d.conns {
		if c.role == roleHolder && c.pid > 0 {
			holders = append(holders, c)
		}
	}
	d.mu.Unlock()

	readings := make(map[*conn]float64, len(holders))
	sampled := make(map[*conn]bool, len(holders))
	for _, c := range holders {
		if frac, ok := d.procSampler.CPUFraction(c.pid); ok {
			readings[c] = frac
			sampled[c] = true
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range holders {
		if c.role != roleHolder || !sampled[c] {
			continue
		}
		if readings[c] < threshold {
			if c.lowSince.IsZero() {
				c.lowSince = now
			}
			if now.Sub(c.lowSince) >= window {
				c.stalled = true
			}
		} else {
			c.lowSince = time.Time{}
			c.stalled = false
		}
	}
}
