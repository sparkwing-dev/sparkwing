package wingd

import (
	"context"
	"time"
)

const descendantStallWindowMultiplier = 4

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
		if c.role == roleHolder && c.pid > 0 && c.drawsAdmission() {
			holders = append(holders, c)
		}
	}
	d.mu.Unlock()

	holderPIDs := make([]int, 0, len(holders))
	holderByPID := make(map[int][]*conn, len(holders))
	for _, c := range holders {
		holderPIDs = append(holderPIDs, c.pid)
		holderByPID[c.pid] = append(holderByPID[c.pid], c)
	}

	readings := make(map[*conn]ProcUsage, len(holders))
	sampled := make(map[*conn]bool, len(holders))
	if batch, ok := d.procSampler.(ProcBatchSampler); ok {
		for pid, usage := range batch.CPUUsages(holderPIDs) {
			for _, c := range holderByPID[pid] {
				readings[c] = usage
				sampled[c] = true
			}
		}
	} else {
		for _, c := range holders {
			if usage, ok := d.procSampler.CPUUsage(c.pid); ok {
				readings[c] = usage
				sampled[c] = true
			}
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range holders {
		if c.role != roleHolder || !sampled[c] {
			continue
		}
		usage := readings[c]
		stallWindow := window
		if usage.HasDescendant {
			stallWindow *= descendantStallWindowMultiplier
		}
		if usage.Fraction < threshold {
			if c.lowSince.IsZero() {
				c.lowSince = now
			}
			if now.Sub(c.lowSince) >= stallWindow {
				c.stalled = true
			}
		} else {
			c.lowSince = time.Time{}
			c.stalled = false
		}
	}
}

func (c *conn) drawsAdmission() bool {
	return c.resources.Cores > 0 || c.resources.MemoryBytes > 0 || len(c.sems) > 0
}
