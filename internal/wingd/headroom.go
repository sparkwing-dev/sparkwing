package wingd

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

const loadEMAAlpha = 0.4

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

func (d *Daemon) refreshHeadroom() {
	d.refreshHostSample(false)
}

func (d *Daemon) refreshHostSample(refreshCapacity bool) {
	roots, cohort := d.holderSample()
	stat, ownedBusy, ownedMeasured, err := d.sampleHostAndOwned(roots)
	if err != nil {
		d.cfg.logf("host sample: %v", err)
		return
	}
	if refreshCapacity {
		d.applyCapacity(stat)
	}
	stat = d.container.apply(stat)
	d.applyHeadroomSample(stat, cohort, ownedBusy, ownedMeasured)
}

func (d *Daemon) sampleHostAndOwned(roots []int) (HostStat, float64, bool, error) {
	if paired, ok := d.sampler.(pairedHostOwnedSampler); ok {
		return paired.SampleWithOwned(roots)
	}
	stat, err := d.sampler.Sample()
	if err != nil {
		return stat, 0, false, err
	}
	if len(roots) == 0 {
		return stat, 0, true, nil
	}
	owned, measured := d.ownedSampler.CPUUsage(roots)
	return stat, owned, measured, nil
}

func (d *Daemon) applyHeadroom(stat HostStat) {
	d.applyHeadroomSample(stat, nil, 0, false)
}

func (d *Daemon) applyHeadroomSample(stat HostStat, sampled holderCohort, ownedBusy float64, ownedMeasured bool) {
	d.mu.Lock()
	if !sameHolderCohort(sampled, d.holderCohortLocked()) {
		ownedBusy = 0
		ownedMeasured = false
	}
	now := d.now()
	if stat.LoadMeasured || stat.MemoryMeasured {
		d.measuredAt = now
	}

	rawExternal := coresExternal(stat, stat.BusyCores, ownedBusy, ownedMeasured)
	if !d.loadInit {
		d.smoothedLoad = stat.LoadAverage
		d.loadInit = true
	} else {
		d.smoothedLoad = loadEMAAlpha*stat.LoadAverage + (1-loadEMAAlpha)*d.smoothedLoad
	}
	if !stat.CPUMeasured {
		d.smoothedExternal = 0
		d.externalInit = false
	} else if !d.externalInit {
		d.smoothedExternal = rawExternal
		d.externalInit = true
	} else {
		d.smoothedExternal = loadEMAAlpha*rawExternal + (1-loadEMAAlpha)*d.smoothedExternal
	}
	load, externalCores := d.smoothedLoad, d.smoothedExternal

	usedCores, usedMem := d.usedLocked()
	frac := d.cfg.headroomFraction()

	reservedCores := frac * stat.TotalCores
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
	d.updateContentionLocked(saturated, d.cfg.sampleInterval().Milliseconds(), now)

	coresBand := math.Max(0.5, 0.05*stat.TotalCores)
	memBand := uint64(0.05 * float64(stat.TotalMemoryBytes))
	changed := !d.headroomInit ||
		math.Abs(targetCores-d.appliedCores) >= coresBand ||
		absDiffU(targetMem, d.appliedMem) >= memBand ||
		d.cpuMeasured != stat.CPUMeasured ||
		d.memMeasured != stat.MemoryMeasured ||
		now.Sub(d.headroomAt) >= d.cfg.headroomMaxAge()
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
	d.headroomAt = now
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
	if len(events) == 0 {
		deliveries = append(deliveries, d.waiterDeliveriesLocked()...)
	}
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	d.cfg.logf("headroom: %.1f cores grantable (reserve %.1f, external %s)", targetCores, reservedCores,
		externalWord(stat.CPUMeasured, fmt.Sprintf("%.1f", externalCores)))
	if !stat.MemoryMeasured {
		d.cfg.logf("headroom: memory external unmeasured (host sensor unavailable); none subtracted")
	}
	d.flush(deliveries, snap)
}

func headroomFromReserveExternal(total, reserved, external uint64) uint64 {
	avail := int64(total) - int64(reserved) - int64(external)
	if avail < 0 {
		return 0
	}
	return uint64(avail)
}

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

func coresExternal(stat HostStat, busy, ownedBusy float64, ownedMeasured bool) float64 {
	if !stat.CPUMeasured {
		return 0
	}
	if !ownedMeasured {
		return busy
	}
	external := busy - ownedBusy
	if external < 0 {
		return 0
	}
	return external
}

type holderCohort map[*conn]int

func (d *Daemon) holderSample() ([]int, holderCohort) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cohort := d.holderCohortLocked()
	seen := map[int]struct{}{}
	for _, pid := range cohort {
		seen[pid] = struct{}{}
	}
	pids := make([]int, 0, len(seen))
	for pid := range seen {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, cohort
}

func (d *Daemon) holderCohortLocked() holderCohort {
	cohort := holderCohort{}
	for _, c := range d.byRun {
		if c.role == roleHolder && c.pid > 0 {
			cohort[c] = c.pid
		}
	}
	return cohort
}

func sameHolderCohort(a, b holderCohort) bool {
	if len(a) != len(b) {
		return false
	}
	for c, pid := range a {
		if b[c] != pid {
			return false
		}
	}
	return true
}

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

func externalWord(measured bool, value string) string {
	if !measured {
		return "unmeasured"
	}
	return value
}

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
