package wingd

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"time"
)

const loadEMAAlpha = 0.4

const unattributedResidual = 0.05

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
	roots := d.holderSample()
	stat, ownedByRoot, ownedMeasured, err := d.sampleHostAndOwned(roots)
	if err != nil {
		d.cfg.logf("host sample: %v", err)
		return
	}
	if refreshCapacity {
		d.applyCapacity(stat)
	}
	stat = d.container.apply(stat)
	d.applyHeadroomSample(stat, ownedByRoot, ownedMeasured)
}

func (d *Daemon) sampleHostAndOwned(roots []OwnedRoot) (HostStat, map[int]float64, bool, error) {
	// safety: the container clamp runs on the stat after this call, so reading
	// TotalCores off the returned stat would bound owned CPU at the machine's
	// capacity rather than at the smaller one admission actually hands out.
	arbitrated := d.container.arbitratedCores(float64(runtime.NumCPU()))
	if paired, ok := d.sampler.(pairedHostOwnedSampler); ok {
		return paired.SampleWithOwned(roots, arbitrated)
	}
	stat, err := d.sampler.Sample()
	if err != nil {
		return stat, nil, false, err
	}
	// safety: a reading with nothing held still reaches the sampler, because that
	// is the reading where it drops what it measured and moves its clock. Returning
	// here instead freezes both, and holding a run again then charges it every
	// interval nobody held it.
	byRoot, measured := d.ownedSampler.CPUUsage(roots, arbitrated)
	return stat, byRoot, measured, nil
}

func (d *Daemon) applyHeadroom(stat HostStat) {
	d.applyHeadroomSample(stat, nil, true)
}

func (d *Daemon) applyHeadroomSample(stat HostStat, ownedByRoot map[int]float64, ownedMeasured bool) {
	d.mu.Lock()
	now := d.now()
	ownedBusy, withoutProcess, awaitingMeasure, processGone := d.ownedBusyLocked(ownedByRoot)
	impossible := false
	if stat.CPUMeasured && ownedBusy > stat.BusyCores {
		// safety: this daemon's runs cannot have used more CPU than the host ran. A
		// larger figure is impossible, and trimming it to fit would leave external at
		// zero -- which is the over-admission it was meant to stop -- so the reading
		// is treated as one that measured nothing and charges the host in full.
		ownedBusy, ownedMeasured, impossible = 0, false, true
	}
	if stat.CPUMeasured {
		d.attribution.samples++
		// safety: one reading is counted under one cause, worst first, so the
		// counts stay disjoint. A sampler that read nothing explains every run's
		// missing figure, so the per-run causes say nothing more.
		switch {
		case !ownedMeasured, impossible:
			d.attribution.samplerUnreadable++
		case withoutProcess:
			d.attribution.runsWithoutProcess++
		case processGone:
			d.attribution.runsProcessGone++
		case awaitingMeasure:
			d.attribution.runsAwaitingMeasure++
		}
		attributed := ownedMeasured && !impossible && !withoutProcess && !awaitingMeasure && !processGone
		if attributed {
			d.attribution.attributed++
		}
		unattributed := 0.0
		if !attributed {
			unattributed = 1
		}
		if !d.unattributedInit {
			d.smoothedUnattributed, d.unattributedInit = unattributed, true
		} else {
			d.smoothedUnattributed = loadEMAAlpha*unattributed + (1-loadEMAAlpha)*d.smoothedUnattributed
		}
	}
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
		d.smoothedUnattributed = 0
		d.unattributedInit = false
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
	d.externalAttributed = d.smoothedUnattributed < unattributedResidual
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

type externalAttribution struct {
	samples             int64
	samplerUnreadable   int64
	runsWithoutProcess  int64
	runsAwaitingMeasure int64
	runsProcessGone     int64
	attributed          int64
}

func (d *Daemon) ownedBusyLocked(ownedByRoot map[int]float64) (owned float64, withoutProcess, awaitingMeasure, processGone bool) {
	counted := map[int]struct{}{}
	for _, c := range d.byRun {
		if c.role != roleHolder {
			continue
		}
		if c.pid <= 0 {
			withoutProcess = true
			continue
		}
		if _, done := counted[c.pid]; done {
			continue
		}
		counted[c.pid] = struct{}{}
		cores, measured := ownedByRoot[c.pid]
		if !measured {
			// safety: a run this daemon has already measured, whose process is now
			// gone while it still holds, is a run that died without releasing. That
			// is a fault, where a run waiting for its second reading is not, and the
			// two are only separable by remembering which pids had a figure.
			if _, seen := d.measuredPIDs[c.pid]; seen {
				processGone = true
			} else {
				awaitingMeasure = true
			}
			continue
		}
		d.measuredPIDs[c.pid] = struct{}{}
		owned += cores
	}
	for pid := range d.measuredPIDs {
		if _, held := counted[pid]; !held {
			delete(d.measuredPIDs, pid)
		}
	}
	return owned, withoutProcess, awaitingMeasure, processGone
}

func (d *Daemon) holderSample() []OwnedRoot {
	d.mu.Lock()
	defer d.mu.Unlock()
	since := map[int]time.Time{}
	for _, c := range d.byRun {
		if c.role != roleHolder || c.pid <= 0 {
			continue
		}
		// safety: two runs sharing a process tree are one root, and the earlier
		// hold bounds how old that tree's processes can be.
		if held, seen := since[c.pid]; !seen || c.startAt.Before(held) {
			since[c.pid] = c.startAt
		}
	}
	pids := make([]int, 0, len(since))
	for pid := range since {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	roots := make([]OwnedRoot, 0, len(pids))
	for _, pid := range pids {
		roots = append(roots, OwnedRoot{PID: pid, HeldSince: since[pid]})
	}
	return roots
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
