package wingd

import (
	"math"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const (
	capacityCoreBand = 0.5

	capacityBandFraction = 0.05
)

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

func (d *Daemon) applyCapacity(stat HostStat) {
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
