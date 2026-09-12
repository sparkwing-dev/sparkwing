package wingd

import (
	"runtime"
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
	SampleWithOwned(roots []OwnedRoot) (HostStat, map[int]float64, bool, error)
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

// OwnedRoot is one run's root process and the moment the daemon began holding
// that run, which bounds how old the run's processes can be.
type OwnedRoot struct {
	PID   int
	Since time.Time
}

type OwnedCPUSampler interface {
	// CPUUsage reports the CPU each root's process tree ran, keyed by the
	// root pid the caller asked about. A process under more than one root
	// counts once, against its nearest ancestor root. A root is in the map
	// only where a figure could be computed for it, so a missing key means
	// no reading for that root this call and a zero value means it ran no
	// measurable CPU. measured reports whether the host's process table was
	// read at all; false leaves byRoot empty and says nothing about any
	// individual root.
	//
	// A process the sampler has no previous reading for has run all of its
	// CPU since the reading before this one, so its whole total belongs to
	// this window rather than to no window at all.
	CPUUsage(roots []OwnedRoot) (byRoot map[int]float64, measured bool)
}

type ownedProcSampler struct {
	//lint:ignore U1000 used by platform implementations
	mu   sync.Mutex
	last map[processIdentity]cpuSample
}

func newOwnedCPUSampler() *ownedProcSampler {
	return &ownedProcSampler{last: map[processIdentity]cpuSample{}}
}

func (s *ownedProcSampler) CPUUsage(roots []OwnedRoot) (map[int]float64, bool) {
	return s.sampleOwned(roots)
}

func newProcSampler() *procSampler {
	return &procSampler{
		last: map[int]cpuSample{},
		tree: map[int]map[int]struct{}{},
	}
}

func (p *procSampler) CPUUsage(pid int) (ProcUsage, bool) { return p.sample(pid) }

func (p *procSampler) CPUUsages(pids []int) map[int]ProcUsage { return p.sampleMany(pids) }

const ownedFirstSightWindow = 30 * time.Second

type processIdentity struct {
	pid        int
	startTicks uint64
}

type ownedProcess struct {
	parentPID  int
	identity   processIdentity
	cpuSeconds float64
}

func ownersByNearestRoot(parentOf map[int]int, rootPIDs map[int]struct{}) map[int]int {
	owners := make(map[int]int, len(parentOf))
	unowned := make(map[int]struct{}, len(parentOf))
	var chain []int
	for pid := range parentOf {
		chain = chain[:0]
		root, found := 0, false
		for current := pid; ; {
			if known, ok := owners[current]; ok {
				root, found = known, true
				break
			}
			if _, ok := unowned[current]; ok {
				break
			}
			if _, ok := rootPIDs[current]; ok {
				root, found = current, true
				chain = append(chain, current)
				break
			}
			chain = append(chain, current)
			next, ok := parentOf[current]
			if !ok || next <= 0 || len(chain) > len(parentOf) {
				break
			}
			current = next
		}
		for _, walked := range chain {
			if found {
				owners[walked] = root
			} else {
				unowned[walked] = struct{}{}
			}
		}
	}
	return owners
}

func ownedProcessOwners(roots []OwnedRoot, processes map[int]ownedProcess) map[processIdentity]int {
	parentOf := make(map[int]int, len(processes))
	for processID, process := range processes {
		parentOf[processID] = process.parentPID
	}
	rootPIDs := make(map[int]struct{}, len(roots))
	for _, root := range roots {
		if _, ok := processes[root.PID]; ok {
			rootPIDs[root.PID] = struct{}{}
		}
	}
	byPID := ownersByNearestRoot(parentOf, rootPIDs)
	owners := make(map[processIdentity]int, len(byPID))
	for processID, root := range byPID {
		owners[processes[processID].identity] = root
	}
	return owners
}

func ownedCPUByRoot(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	owners map[processIdentity]int,
	roots []OwnedRoot,
	now time.Time,
	maxWindow time.Duration,
) (map[int]float64, map[processIdentity]cpuSample) {
	windows := rootWindows(previous, processes, roots, now, maxWindow)
	next := make(map[processIdentity]cpuSample, len(owners))
	byRoot := make(map[int]float64, len(owners))
	unbounded := map[int]struct{}{}
	for identity, root := range owners {
		process, ok := processes[identity.pid]
		if !ok {
			continue
		}
		next[identity] = cpuSample{cpuSeconds: process.cpuSeconds, at: now}
		window, watched := windows[root]
		if !watched {
			continue
		}
		if _, seeded := byRoot[root]; !seeded {
			byRoot[root] = 0
		}
		if prior, ok := previous[identity]; ok {
			wall := now.Sub(prior.at).Seconds()
			delta := process.cpuSeconds - prior.cpuSeconds
			if wall <= 0 || delta < 0 {
				continue
			}
			byRoot[root] += delta / wall
			continue
		}
		// safety: a process first seen here ran all its CPU inside this window, so
		// the whole total belongs here. A total larger than the window could hold
		// proves it is older, joined the tree rather than started in it, and has an
		// age nothing here can bound -- so the tree's whole figure goes with it.
		if window <= 0 || process.cpuSeconds > window*hostCoreCount() {
			unbounded[root] = struct{}{}
			continue
		}
		byRoot[root] += process.cpuSeconds / window
	}
	for root := range unbounded {
		delete(byRoot, root)
	}
	return byRoot, next
}

func hostCoreCount() float64 {
	if n := runtime.NumCPU(); n > 0 {
		return float64(n)
	}
	return 1
}

func rootWindows(
	previous map[processIdentity]cpuSample,
	processes map[int]ownedProcess,
	roots []OwnedRoot,
	now time.Time,
	maxWindow time.Duration,
) map[int]float64 {
	windows := make(map[int]float64, len(roots))
	for _, root := range roots {
		process, ok := processes[root.PID]
		if !ok {
			continue
		}
		if prior, ok := previous[process.identity]; ok {
			if wall := now.Sub(prior.at).Seconds(); wall > 0 {
				windows[root.PID] = wall
			}
			continue
		}
		// safety: with no previous reading for the root itself the daemon was not
		// watching this tree, so only a run that began holding inside the window can
		// have its total attributed to it. An older one stays unmeasured rather than
		// credited a lifetime average that no longer describes it.
		held := now.Sub(root.Since)
		if !root.Since.IsZero() && held > 0 && held <= maxWindow {
			windows[root.PID] = held.Seconds()
		}
	}
	return windows
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
	roots []OwnedRoot,
	now time.Time,
	totalCores float64,
) (float64, bool, map[int]float64, bool) {
	if len(processes) == 0 || len(previous) == 0 || elapsedSeconds <= 0 {
		return 0, false, nil, false
	}
	fractions := make(map[int]float64, len(processes))
	var host float64
	for processID, process := range processes {
		// bug: this snapshot carries no process start time, so a pid the OS
		// recycles within one interval reads as the dead process continuing.
		// Only a falling cpuSeconds catches it.
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
		return host, true, nil, true
	}
	rootPIDs := make(map[int]struct{}, len(roots))
	watched := make(map[int]float64, len(roots))
	for _, root := range roots {
		if _, ok := processes[root.PID]; !ok {
			continue
		}
		rootPIDs[root.PID] = struct{}{}
		if _, based := previous[root.PID]; based {
			watched[root.PID] = elapsedSeconds
			continue
		}
		held := now.Sub(root.Since).Seconds()
		if !root.Since.IsZero() && held > 0 && held <= ownedFirstSightWindow.Seconds() {
			watched[root.PID] = held
		}
	}
	parentOf := make(map[int]int, len(processes))
	for processID, process := range processes {
		parentOf[processID] = process.parentPID
	}
	owners := ownersByNearestRoot(parentOf, rootPIDs)
	byRoot := make(map[int]float64, len(rootPIDs))
	for root := range watched {
		byRoot[root] = 0
	}
	for processID, process := range processes {
		root, ok := owners[processID]
		if !ok {
			continue
		}
		window, ok := watched[root]
		if !ok {
			continue
		}
		if fraction, measured := fractions[processID]; measured {
			byRoot[root] += fraction
			continue
		}
		if _, seen := previous[processID]; !seen && window > 0 {
			byRoot[root] += process.cpuSeconds / window
		}
	}
	for root, owned := range byRoot {
		byRoot[root] = clampCores(owned, totalCores)
	}
	return host, true, byRoot, true
}

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
