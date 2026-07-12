package wingd

import (
	"context"
	"math"
	"time"
)

// loadEMAAlpha weights the newest load sample when smoothing. A modest
// weight damps momentary spikes so admission headroom does not flap.
const loadEMAAlpha = 0.4

// sampleLoop periodically re-reads host pressure and feeds it into the
// ledger's headroom until the context is cancelled or the daemon stops.
func (d *Daemon) sampleLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.sampleInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-t.C:
			d.refreshHeadroom()
		}
	}
}

// refreshHeadroom samples the host and applies the result. Sampler errors
// are logged and leave the last headroom in force.
func (d *Daemon) refreshHeadroom() {
	stat, err := d.sampler.Sample()
	if err != nil {
		d.cfg.logf("host sample: %v", err)
		return
	}
	d.applyHeadroom(d.container.apply(stat))
}

// applyHeadroom converts a host reading into a ledger headroom ceiling:
// total capacity minus the reserved margin minus whatever load and memory
// the machine is under from work the daemon did not admit. It only pushes
// a change past a deadband, so small wiggles never disturb admission.
func (d *Daemon) applyHeadroom(stat HostStat) {
	d.mu.Lock()

	if !d.loadInit {
		d.smoothedLoad = stat.LoadAverage
		d.loadInit = true
	} else {
		d.smoothedLoad = loadEMAAlpha*stat.LoadAverage + (1-loadEMAAlpha)*d.smoothedLoad
	}
	load := d.smoothedLoad

	usedCores, usedMem := d.usedLocked()
	frac := d.cfg.headroomFraction()

	reservedCores := frac * stat.TotalCores
	externalCores := load - usedCores
	if externalCores < 0 {
		externalCores = 0
	}
	targetCores := stat.TotalCores - reservedCores - externalCores
	if targetCores < 0 {
		targetCores = 0
	}

	reservedMem, externalMem := memReserveAndExternal(stat, usedMem, frac)
	targetMem := memHeadroom(stat, usedMem, frac)

	d.reservedCores = reservedCores
	d.externalCores = externalCores
	d.reservedMem = reservedMem
	d.externalMem = externalMem

	grantable := stat.TotalCores - reservedCores
	saturated := grantable > 0 && externalCores >= contentionSaturationFraction*grantable
	d.updateContentionLocked(saturated, d.cfg.sampleInterval().Milliseconds(), d.now())

	coresBand := math.Max(0.5, 0.05*stat.TotalCores)
	memBand := uint64(0.05 * float64(stat.TotalMemoryBytes))
	changed := !d.headroomInit ||
		math.Abs(targetCores-d.appliedCores) >= coresBand ||
		absDiffU(targetMem, d.appliedMem) >= memBand
	if !changed {
		d.mu.Unlock()
		return
	}
	d.appliedCores = targetCores
	d.appliedMem = targetMem
	d.headroomInit = true

	events, err := d.ledger.SetHeadroom(targetCores, targetMem)
	if err != nil {
		d.mu.Unlock()
		d.cfg.logf("set headroom: %v", err)
		return
	}
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	d.cfg.logf("headroom: %.1f cores grantable (reserve %.1f, external %.1f)", targetCores, reservedCores, externalCores)
	d.flush(deliveries, snap)
}

// memHeadroom is the memory ceiling: total minus the reserve minus memory
// consumed by processes the daemon did not admit.
func memHeadroom(stat HostStat, usedMem uint64, frac float64) uint64 {
	reserved, external := memReserveAndExternal(stat, usedMem, frac)
	avail := int64(stat.TotalMemoryBytes) - int64(reserved) - int64(external)
	if avail < 0 {
		return 0
	}
	return uint64(avail)
}

// memReserveAndExternal decomposes the memory headroom into its reserve
// margin and the memory consumed by processes the daemon did not admit,
// for the queue view.
func memReserveAndExternal(stat HostStat, usedMem uint64, frac float64) (reserved, external uint64) {
	reserved = uint64(frac * float64(stat.TotalMemoryBytes))
	if stat.TotalMemoryBytes >= stat.FreeMemoryBytes {
		consumed := stat.TotalMemoryBytes - stat.FreeMemoryBytes
		if consumed > usedMem {
			external = consumed - usedMem
		}
	}
	return reserved, external
}

// usedLocked sums the host resources currently held across all leases.
func (d *Daemon) usedLocked() (cores float64, mem uint64) {
	snap := d.ledger.Snapshot()
	var milli int64
	for _, ls := range snap.Leases {
		milli += ls.MilliCores
		mem += ls.MemoryBytes
	}
	return float64(milli) / 1000.0, mem
}

func absDiffU(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}
