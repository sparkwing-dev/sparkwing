package wingd

import (
	"context"
	"fmt"
	"math"
	"time"
)

// loadEMAAlpha weights the newest load sample when smoothing. A modest
// weight damps momentary spikes so admission headroom does not flap.
const loadEMAAlpha = 0.4

// sampleLoop periodically re-reads host pressure and feeds it into the
// ledger's headroom until the context is cancelled or the daemon stops. On a
// slower cadence it also re-derives machine capacity, so an instance resize
// or a cgroup-quota edit is picked up without a restart. Capacity is refreshed
// before headroom in the same tick, so a grow's promotion runs against the new
// total.
func (d *Daemon) sampleLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.sampleInterval())
	defer t.Stop()
	capEvery := d.cfg.capacityInterval()
	lastCap := d.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-t.C:
			now := d.now()
			refreshCapacity := now.Sub(lastCap) >= capEvery
			d.refreshHostSample(refreshCapacity)
			if refreshCapacity {
				lastCap = now
			}
		}
	}
}

// refreshHeadroom samples the host and applies the result. Sampler errors
// are logged and leave the last headroom in force.
func (d *Daemon) refreshHeadroom() {
	d.refreshHostSample(false)
}

// refreshHostSample uses one host reading for every calculation on a sample
// tick. Stateful CPU counters advance when sampled, so separate capacity and
// headroom reads would measure utilization over the few moments between them.
func (d *Daemon) refreshHostSample(refreshCapacity bool) {
	stat, err := d.sampler.Sample()
	if err != nil {
		d.cfg.logf("host sample: %v", err)
		return
	}
	if refreshCapacity {
		d.applyCapacity(stat)
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
		d.smoothedBusy = stat.BusyCores
		d.loadInit = true
	} else {
		d.smoothedLoad = loadEMAAlpha*stat.LoadAverage + (1-loadEMAAlpha)*d.smoothedLoad
		d.smoothedBusy = loadEMAAlpha*stat.BusyCores + (1-loadEMAAlpha)*d.smoothedBusy
	}
	load, busy := d.smoothedLoad, d.smoothedBusy

	usedCores, usedMem := d.usedLocked()
	frac := d.cfg.headroomFraction()

	reservedCores := frac * stat.TotalCores
	externalCores := coresExternal(stat, busy, usedCores)
	reservedMem, externalMem := memReserveAndExternal(stat, usedMem, frac)

	admitExternalCores, admitExternalMem := externalCores, externalMem
	if d.cfg.Budget.IgnoreExternal {
		admitExternalCores, admitExternalMem = 0, 0
	}
	targetCores := stat.TotalCores - reservedCores - admitExternalCores
	if targetCores < 0 {
		targetCores = 0
	}
	targetMem := headroomFromReserveExternal(stat.TotalMemoryBytes, reservedMem, admitExternalMem)

	grantable := stat.TotalCores - reservedCores
	saturated := grantable > 0 && coresContention(stat, load, usedCores) >= contentionSaturationFraction*grantable
	d.updateContentionLocked(saturated, d.cfg.sampleInterval().Milliseconds(), d.now())

	coresBand := math.Max(0.5, 0.05*stat.TotalCores)
	memBand := uint64(0.05 * float64(stat.TotalMemoryBytes))
	changed := !d.headroomInit ||
		math.Abs(targetCores-d.appliedCores) >= coresBand ||
		absDiffU(targetMem, d.appliedMem) >= memBand ||
		d.cpuMeasured != stat.CPUMeasured ||
		d.memMeasured != stat.MemoryMeasured
	if !changed {
		d.mu.Unlock()
		return
	}
	// safety: the decomposition is stored only past the deadband, with the
	// headroom it produced. The queue view subtracts these from capacity to
	// show what is available, so a fresher sample stored here would print a
	// table that does not balance against the headroom admission is on.
	d.reservedCores = reservedCores
	d.externalCores = externalCores
	d.reservedMem = reservedMem
	d.externalMem = externalMem
	d.cpuMeasured = stat.CPUMeasured
	d.memMeasured = stat.MemoryMeasured
	d.headroomAt = d.now()
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
	d.cfg.logf("headroom: %.1f cores grantable (reserve %.1f, external %s)", targetCores, reservedCores,
		externalWord(stat.LoadMeasured, fmt.Sprintf("%.1f", externalCores)))
	if !stat.MemoryMeasured {
		d.cfg.logf("headroom: memory external unmeasured (host sensor unavailable); none subtracted")
	}
	d.flush(deliveries, snap)
}

// headroomFromReserveExternal is the memory ceiling: total minus the
// reserve minus the external memory admission subtracts. The external term
// is zero when the operator has set ignore-external.
func headroomFromReserveExternal(total, reserved, external uint64) uint64 {
	avail := int64(total) - int64(reserved) - int64(external)
	if avail < 0 {
		return 0
	}
	return uint64(avail)
}

// memReserveAndExternal decomposes the memory headroom into its reserve
// margin and the memory consumed by processes the daemon did not admit,
// for the queue view. An unmeasured reading yields no external term at
// all: subtracting a number the sampler never read is what pinned memory
// headroom at zero on every box, and admission may not charge a run
// against pressure nobody looked at.
func memReserveAndExternal(stat HostStat, usedMem uint64, frac float64) (reserved, external uint64) {
	reserved = uint64(frac * float64(stat.TotalMemoryBytes))
	if !stat.MemoryMeasured {
		return reserved, 0
	}
	if stat.TotalMemoryBytes >= stat.FreeMemoryBytes {
		consumed := stat.TotalMemoryBytes - stat.FreeMemoryBytes
		if consumed > usedMem {
			external = consumed - usedMem
		}
	}
	return reserved, external
}

// coresExternal is the CPU the daemon did not admit: smoothed host
// utilization in cores minus what its own leases hold, floored at zero. It
// is zero when utilization is unmeasured, on the same rule as the memory
// term -- admission may not charge a run against pressure nobody read.
//
// It reads utilization rather than the run queue because this figure is
// subtracted from a core count. The run queue counts threads waiting,
// including on uninterruptible I/O, so on an I/O-bound box it runs far
// above the cores in use and erases a machine that is mostly idle.
func coresExternal(stat HostStat, busy, usedCores float64) float64 {
	if !stat.CPUMeasured {
		return 0
	}
	external := busy - usedCores
	if external < 0 {
		return 0
	}
	return external
}

// coresContention is the run-queue pressure the daemon did not admit. It
// answers a different question from [coresExternal]: not how much of the
// machine is consumed, but how hard work is queueing for it. Threads
// waiting on I/O belong in that answer, which is why this one reads the
// load average and the capacity subtraction does not.
func coresContention(stat HostStat, load, usedCores float64) float64 {
	if !stat.LoadMeasured {
		return 0
	}
	contention := load - usedCores
	if contention < 0 {
		return 0
	}
	return contention
}

// externalWord renders an external-load figure for a log line, or the
// word "unmeasured" when the sensor could not read the dimension, so a
// blind reading never reads as a number in the log either.
func externalWord(measured bool, value string) string {
	if !measured {
		return "unmeasured"
	}
	return value
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
