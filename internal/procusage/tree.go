package procusage

import (
	"math"
	"time"
)

type Reading struct {
	Start, End                time.Time
	CPUMillicores             int64
	RSSBytes                  int64
	CPUQuality, MemoryQuality string
	Processes                 int
	CPURateTiming             *CPURateTiming `json:"cpu_rate_timing,omitempty"`
}

type Tree struct {
	PID           int
	root          Identity
	owned         map[Identity]bool
	previous      map[Identity]Process
	previousStart time.Time
	previousEnd   time.Time
}

func (t *Tree) Observe(snapshot Snapshot) Reading {
	r := Reading{Start: t.previousStart, End: snapshot.End, CPUQuality: "unavailable", MemoryQuality: "unavailable"}
	if r.Start.IsZero() {
		r.Start = snapshot.Start
	}
	root, exists := snapshot.Processes[t.PID]
	if !snapshot.Available || !exists || !root.IdentityAvailable || !root.AncestryAvailable {
		t.previous = nil
		t.previousStart = snapshot.Start
		t.previousEnd = snapshot.End
		return r
	}
	if t.owned == nil {
		t.owned = make(map[Identity]bool)
		t.root = root.Identity
	}
	if root.Identity != t.root {
		t.previous = nil
		return r
	}
	selected, ancestryGap := t.selectProcesses(snapshot)
	next := make(map[Identity]Process, len(selected))
	r.MemoryQuality, r.CPUQuality = "sampled", "sampled"
	if ancestryGap {
		r.MemoryQuality, r.CPUQuality = "partial", "partial"
	}
	var cores float64
	var cpuReadings, memoryReadings int
	for pid := range selected {
		process := snapshot.Processes[pid]
		r.Processes++
		if !process.IdentityAvailable || !process.AncestryAvailable {
			r.MemoryQuality, r.CPUQuality = "partial", "partial"
			continue
		}
		t.owned[process.Identity] = true
		next[process.Identity] = process
		if process.RSSAvailable && process.RSS >= 0 && process.RSS <= math.MaxInt64-r.RSSBytes {
			r.RSSBytes += process.RSS
			memoryReadings++
		} else {
			r.MemoryQuality = "partial"
		}
		previous, ok := t.previous[process.Identity]
		if ok && previous.ReapedAvailable && process.ReapedAvailable && process.ReapedCPU != previous.ReapedCPU {
			// safety: reaped CPU exposes unsampled work without placing it in this interval.
			r.CPUQuality = "partial"
		}
		elapsed := process.ReadAt.Sub(previous.ReadAt)
		if ok && previous.CPUAvailable && process.CPUAvailable && process.CPU >= previous.CPU && elapsed > 0 {
			cores += float64(process.CPU-previous.CPU) / float64(elapsed)
			cpuReadings++
		} else {
			r.CPUQuality = "partial"
		}
	}
	for identity := range t.previous {
		if _, ok := next[identity]; !ok {
			r.CPUQuality = "partial"
		}
	}
	if cpuReadings == 0 {
		r.CPUQuality = "unavailable"
	} else {
		timing := CPURateTiming{
			PreviousScan: ScanWindow{Start: t.previousStart, End: t.previousEnd},
			CurrentScan:  ScanWindow{Start: snapshot.Start, End: snapshot.End},
		}
		if err := timing.Validate(); err != nil {
			r.CPUQuality = "unavailable"
		} else {
			r.CPURateTiming = &timing
		}
	}
	if memoryReadings == 0 {
		r.MemoryQuality = "unavailable"
	}
	if cores < float64(math.MaxInt64)/1000 {
		r.CPUMillicores = int64(cores * 1000)
	} else {
		r.CPUQuality = "unavailable"
	}
	t.previous, t.previousStart = next, snapshot.Start
	t.previousEnd = snapshot.End
	return r
}

func (t *Tree) selectProcesses(snapshot Snapshot) (map[int]bool, bool) {
	root, exists := snapshot.Processes[t.PID]
	if !snapshot.Available || !exists || !root.IdentityAvailable || !root.AncestryAvailable || t.owned != nil && root.Identity != t.root {
		return nil, true
	}
	selected := make(map[int]bool)
	for identity := range t.owned {
		if process, ok := snapshot.Processes[identity.PID]; ok && !process.IdentityAvailable {
			selected[identity.PID] = true
		}
	}
	for pid, process := range snapshot.Processes {
		if process.Identity == root.Identity || t.owned[process.Identity] {
			selected[pid] = true
		}
	}
	ancestryGap := false
	for changed := true; changed; {
		changed = false
		for pid, process := range snapshot.Processes {
			if !selected[pid] && selected[process.Parent] {
				parent := snapshot.Processes[process.Parent]
				if !parent.IdentityAvailable || !parent.AncestryAvailable || process.IdentityAvailable && process.Birth < parent.Birth {
					ancestryGap = true
					continue
				}
				selected[pid], changed = true, true
			}
		}
	}
	return selected, ancestryGap
}
