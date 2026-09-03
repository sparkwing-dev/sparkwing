package wingd

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

type HostStat struct {
	TotalCores float64

	TotalMemoryBytes uint64

	LoadAverage float64

	BusyCores float64

	FreeMemoryBytes uint64

	LoadMeasured bool

	CPUMeasured bool

	MemoryMeasured bool
}

type HostSampler interface {
	Sample() (HostStat, error)
}

type pairedHostOwnedSampler interface {
	SampleWithOwned(roots []int) (HostStat, float64, bool, error)
}

type hostSamplerOnly struct {
	HostSampler
}

type platformSampler struct {
	cpu cpuTracker

	darwinPrev   map[int]darwinCPUProcess
	darwinPrevAt time.Time
}

func (p *platformSampler) Sample() (HostStat, error) {
	stat, err := sampleHost()
	if err != nil {
		return stat, err
	}
	stat.BusyCores, stat.CPUMeasured = p.cpu.busyCores(stat.TotalCores)
	return stat, nil
}

type ProcSampler interface {
	CPUUsage(pid int) (ProcUsage, bool)
}

type ProcBatchSampler interface {
	CPUUsages(pids []int) map[int]ProcUsage
}

type OwnedCPUSampler interface {
	CPUUsage(pids []int) (fraction float64, measured bool)
}

type ownedProcSampler struct {
	//lint:ignore U1000 used by platform implementations
	mu   sync.Mutex
	last map[processIdentity]cpuSample
}

func newOwnedCPUSampler() *ownedProcSampler {
	return &ownedProcSampler{last: map[processIdentity]cpuSample{}}
}

func (s *ownedProcSampler) CPUUsage(pids []int) (float64, bool) {
	return s.sampleOwned(pids)
}

type ProcUsage struct {
	Fraction      float64
	HasDescendant bool
}

type procSampler struct {
	//lint:ignore U1000 used by platform implementations
	mu   sync.Mutex
	last map[int]cpuSample
	tree map[int]map[int]struct{}
}

type cpuSample struct {
	cpuSeconds float64
	at         time.Time
	startTicks uint64
}

func processCPUFraction(previous, current cpuSample) (float64, bool) {
	if previous.startTicks == 0 || previous.startTicks != current.startTicks {
		return 0, false
	}
	wall := current.at.Sub(previous.at).Seconds()
	delta := current.cpuSeconds - previous.cpuSeconds
	if wall <= 0 || delta < 0 {
		return 0, false
	}
	return delta / wall, true
}

type processIdentity struct {
	pid        int
	startTicks uint64
}

type ownedProcess struct {
	parentPID  int
	identity   processIdentity
	cpuSeconds float64
}

func ownedProcessIdentities(roots []int, processes map[int]ownedProcess) map[processIdentity]struct{} {
	children := map[int][]int{}
	for processID, process := range processes {
		children[process.parentPID] = append(children[process.parentPID], processID)
	}
	owned := map[processIdentity]struct{}{}
	for _, root := range roots {
		if _, ok := processes[root]; !ok {
			continue
		}
		for _, processID := range collectSubtree(root, children) {
			owned[processes[processID].identity] = struct{}{}
		}
	}
	return owned
}

func ownedCPUFromProcesses(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	owned map[processIdentity]struct{},
	now time.Time,
) (float64, bool, map[processIdentity]cpuSample) {
	next := make(map[processIdentity]cpuSample, len(owned))
	var fraction float64
	var measured bool
	for identity := range owned {
		process, ok := processes[identity.pid]
		if !ok || process.identity != identity {
			continue
		}
		next[identity] = cpuSample{cpuSeconds: process.cpuSeconds, at: now}
		prior, ok := previous[identity]
		if !ok {
			continue
		}
		wall := now.Sub(prior.at).Seconds()
		delta := process.cpuSeconds - prior.cpuSeconds
		if wall <= 0 || delta < 0 {
			continue
		}
		fraction += delta / wall
		measured = true
	}
	return fraction, measured, next
}

type darwinCPUProcess struct {
	parentPID int

	cpuSeconds float64
}

func parseDarwinCPUTime(field string) (float64, bool) {
	days := 0.0
	if dash := strings.IndexByte(field, '-'); dash >= 0 {
		parsed, err := strconv.ParseFloat(field[:dash], 64)
		if err != nil {
			return 0, false
		}
		days, field = parsed, field[dash+1:]
	}
	parts := strings.Split(field, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		total = total*60 + value
	}
	return days*86400 + total, true
}

func parseDarwinCPUSnapshot(output string) (map[int]darwinCPUProcess, bool) {
	processes := map[int]darwinCPUProcess{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		processID, pidErr := strconv.Atoi(fields[0])
		parentPID, parentErr := strconv.Atoi(fields[1])
		cpuSeconds, cpuOK := parseDarwinCPUTime(fields[2])
		if pidErr != nil || parentErr != nil || !cpuOK || processID <= 0 || parentPID < 0 {
			continue
		}
		processes[processID] = darwinCPUProcess{parentPID: parentPID, cpuSeconds: cpuSeconds}
	}
	return processes, len(processes) > 0
}

func darwinCPUFromSnapshot(
	processes map[int]darwinCPUProcess,
	previous map[int]darwinCPUProcess,
	elapsedSeconds float64,
	roots []int,
	totalCores float64,
) (float64, bool, float64, bool) {
	if len(processes) == 0 || len(previous) == 0 || elapsedSeconds <= 0 {
		return 0, false, 0, false
	}
	fractions := make(map[int]float64, len(processes))
	children := map[int][]int{}
	var host float64
	for processID, process := range processes {
		children[process.parentPID] = append(children[process.parentPID], processID)
		prior, seen := previous[processID]
		if !seen {
			continue
		}
		delta := process.cpuSeconds - prior.cpuSeconds
		if delta <= 0 {
			continue
		}
		fraction := delta / elapsedSeconds
		fractions[processID] = fraction
		host += fraction
	}
	host = clampCores(host, totalCores)
	if len(roots) == 0 {
		return host, true, 0, true
	}
	ownedIDs := map[int]struct{}{}
	for _, root := range roots {
		if _, ok := processes[root]; !ok {
			return host, true, 0, false
		}
		for _, processID := range collectSubtree(root, children) {
			ownedIDs[processID] = struct{}{}
		}
	}
	var owned float64
	for processID := range ownedIDs {
		owned += fractions[processID]
	}
	return host, true, clampCores(owned, totalCores), true
}

func newProcSampler() *procSampler {
	return &procSampler{
		last: map[int]cpuSample{},
		tree: map[int]map[int]struct{}{},
	}
}

func (p *procSampler) CPUUsage(pid int) (ProcUsage, bool) { return p.sample(pid) }

func (p *procSampler) CPUUsages(pids []int) map[int]ProcUsage { return p.sampleMany(pids) }

func collectSubtree(root int, children map[int][]int) []int {
	var out []int
	seen := map[int]bool{}
	stack := []int{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		stack = append(stack, children[n]...)
	}
	return out
}

func (p *procSampler) forget(pid int) {
	p.mu.Lock()
	delete(p.last, pid)
	delete(p.tree, pid)
	p.mu.Unlock()
}

func trackedPIDs(pids []int) map[int]struct{} {
	tracked := make(map[int]struct{}, len(pids))
	for _, pid := range pids {
		tracked[pid] = struct{}{}
	}
	return tracked
}

func (p *procSampler) pruneTreeLocked(root int, live map[int]struct{}) {
	for pid := range p.tree[root] {
		if _, ok := live[pid]; !ok {
			delete(p.last, pid)
		}
	}
	p.tree[root] = live
}
