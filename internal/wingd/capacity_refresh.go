package wingd

import (
	"math"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const (
	// capacityCoreBand is the smallest core swing that re-derivation acts on,
	// so a fractional measurement wobble never resizes the ledger.
	capacityCoreBand = 0.5
	// capacityBandFraction scales the deadband with the machine, so a large
	// host tolerates a proportionally larger wiggle before a resize.
	capacityBandFraction = 0.05
)

// capacityReading is the machine capacity derived from one host sample: the
// raw host totals, the effective machine size after the container clamp, the
// container ceiling when it binds below the host, and the budget-capped
// totals the ledger is sized against.
type capacityReading struct {
	hostCores       float64
	hostMemory      uint64
	machineCores    float64
	machineMemory   uint64
	containerCores  float64
	containerMemory uint64
	budgetCores     float64
	budgetMemory    uint64
}

// deriveCapacity resolves the machine capacity from a host sample: the host
// total, lowered to the cgroup ceiling on each dimension the container
// constrains below it, then capped by the configured budget. It is the
// single derivation both startup and the periodic re-check use, so a running
// daemon sizes capacity exactly as a fresh one does.
func (d *Daemon) deriveCapacity(stat HostStat) capacityReading {
	r := capacityReading{
		hostCores:     stat.TotalCores,
		hostMemory:    stat.TotalMemoryBytes,
		machineCores:  stat.TotalCores,
		machineMemory: stat.TotalMemoryBytes,
	}
	if ccores, cmem := d.container.capacityLimits(); ccores > 0 || cmem > 0 {
		if ccores > 0 && ccores < r.machineCores {
			r.containerCores = ccores
			r.machineCores = ccores
		}
		if cmem > 0 && cmem < r.machineMemory {
			r.containerMemory = cmem
			r.machineMemory = cmem
		}
	}
	r.budgetCores = d.cfg.Budget.CapCores(r.machineCores)
	r.budgetMemory = d.cfg.Budget.CapMemory(r.machineMemory)
	return r
}

// refreshCapacity re-derives machine capacity from a fresh host sample and
// resizes the ledger when it has moved past the hysteresis band, so a hot VM
// resize or a runtime cgroup-quota edit is picked up without a restart.
//
// A shrink never evicts a running holder: the applied total is floored at the
// cores and memory already granted, so holders drain naturally while
// admission tightens against the smaller machine; the true smaller total
// takes effect on a later tick once enough has drained. A grow applies at
// once, and the headroom refresh that follows in the sample loop promotes any
// waiter the larger capacity now fits.
func (d *Daemon) refreshCapacity() {
	stat, err := d.sampler.Sample()
	if err != nil {
		d.cfg.logf("capacity sample: %v", err)
		return
	}
	cap := d.deriveCapacity(stat)

	d.mu.Lock()
	coreBand := math.Max(capacityCoreBand, capacityBandFraction*d.budgetCores)
	memBand := uint64(capacityBandFraction * float64(d.budgetMemory))
	coresMoved := math.Abs(cap.budgetCores-d.budgetCores) >= coreBand
	memMoved := absDiffU(cap.budgetMemory, d.budgetMemory) >= memBand
	if !coresMoved && !memMoved {
		d.mu.Unlock()
		return
	}

	usedCores, usedMem := d.usedLocked()
	applyCores := math.Max(cap.budgetCores, usedCores)
	applyMem := cap.budgetMemory
	if applyMem < usedMem {
		applyMem = usedMem
	}
	if applyCores == d.budgetCores && applyMem == d.budgetMemory {
		d.mu.Unlock()
		return
	}
	if err := d.ledger.ResizeTotals(applyCores, applyMem); err != nil {
		d.mu.Unlock()
		d.cfg.logf("capacity resize: %v", err)
		return
	}
	oldCores := d.budgetCores
	d.hostCores, d.hostMemory = cap.hostCores, cap.hostMemory
	d.machineCores, d.machineMemory = cap.machineCores, cap.machineMemory
	d.containerCores, d.containerMemory = cap.containerCores, cap.containerMemory
	d.budgetCores, d.budgetMemory = applyCores, applyMem
	d.capacityChange = &wingwire.CapacityChange{
		FromCores: oldCores,
		ToCores:   applyCores,
		AtMS:      d.now().UnixMilli(),
	}
	snap := d.ledger.Snapshot()
	d.mu.Unlock()

	d.cfg.logf("capacity changed: %.1f -> %.1f cores", oldCores, applyCores)
	d.flush(nil, snap)
}
