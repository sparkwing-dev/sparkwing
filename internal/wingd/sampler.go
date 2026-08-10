package wingd

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// HostStat is one reading of the machine's capacity and current
// pressure. TotalCores and TotalMemoryBytes are fixed properties of the
// host; LoadAverage and FreeMemoryBytes move as the machine is used,
// including by processes sparkwing knows nothing about.
type HostStat struct {
	// TotalCores is the machine's logical CPU count.
	TotalCores float64
	// TotalMemoryBytes is the machine's physical memory.
	TotalMemoryBytes uint64
	// LoadAverage is the 1-minute run-queue load average: how many threads
	// are runnable or waiting on uninterruptible I/O. It counts demand, not
	// cores consumed, and the two diverge widely on an I/O-heavy box, so it
	// drives the contention signal and never the capacity subtraction.
	LoadAverage float64
	// BusyCores is host CPU utilization expressed in cores, so 2.5 means
	// two and a half cores' worth of instructions executing. This is the
	// figure admission subtracts from capacity, because it is the one
	// denominated in the same unit as the cores a run is granted.
	BusyCores float64
	// FreeMemoryBytes is memory the OS reports as available for new
	// allocations.
	FreeMemoryBytes uint64
	// LoadMeasured reports that LoadAverage came from a host reading.
	// False means the sampler could not look, so LoadAverage carries no
	// measurement and the queue view prints the dimension as unmeasured
	// instead of a number. A sampler that leaves this false is read as
	// blind, never as idle.
	LoadMeasured bool
	// CPUMeasured is LoadMeasured's counterpart for BusyCores. False
	// subtracts no external cores at all: admission may not charge a run
	// against pressure nobody looked at, and a machine reported full by a
	// sensor that never read is one no run can ever enter.
	CPUMeasured bool
	// MemoryMeasured is LoadMeasured's counterpart for FreeMemoryBytes.
	MemoryMeasured bool
}

// HostSampler reads the machine's capacity and live pressure. The daemon
// samples it at start (for fixed totals) and periodically (for load and
// free memory), feeding the result into the ledger's headroom. Tests
// supply a fake so admission gating is exercised without touching the
// real machine.
type HostSampler interface {
	Sample() (HostStat, error)
}

type pairedHostOwnedSampler interface {
	SampleWithOwned(roots []int) (HostStat, float64, bool, error)
}

type hostSamplerOnly struct {
	HostSampler
}

// platformSampler reads real host metrics for the current OS. It carries
// the CPU tracker across calls because platforms that expose cumulative
// counters derive utilization from the change between two readings, so a
// stateless sampler could only ever report the machine's since-boot
// average rather than what it is doing now.
type platformSampler struct {
	cpu cpuTracker
}

// Sample returns a live [HostStat] for the host it runs on. The first call
// on a delta-based platform leaves CPU unmeasured, having nothing to
// difference against yet; the next one reports.
func (p *platformSampler) Sample() (HostStat, error) {
	stat, err := sampleHost()
	if err != nil {
		return stat, err
	}
	stat.BusyCores, stat.CPUMeasured = p.cpu.busyCores(stat.TotalCores)
	return stat, nil
}

// ProcSampler reads a process tree's recent CPU usage as a fraction of
// one core (1.0 means one core fully busy). The daemon consults it at a
// slow cadence, and only while runs are queued, to tell a working holder
// from one that is alive but wedged. Tests supply a fake so stall
// flagging is exercised deterministically.
type ProcSampler interface {
	// CPUUsage reports the root process and descendant processes' CPU
	// usage, and false when the process tree cannot be sampled -- it is
	// gone, or the platform offers no cheap per-process reading.
	CPUUsage(pid int) (ProcUsage, bool)
}

type ProcBatchSampler interface {
	CPUUsages(pids []int) map[int]ProcUsage
}

// OwnedCPUSampler measures the union of live process trees rooted at pids.
// Overlapping trees count each process once.
type OwnedCPUSampler interface {
	CPUUsage(pids []int) (fraction float64, measured bool)
}

type ownedProcSampler struct {
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

// procSampler is the platform ProcSampler. It carries a small per-pid
// memory of the previous cumulative-CPU reading for platforms that
// derive a rate from two samples (Linux); platforms that expose a live
// percentage (macOS) ignore it.
type procSampler struct {
	mu   sync.Mutex
	last map[int]cpuSample
	tree map[int]map[int]struct{}
}

// cpuSample is one cumulative-CPU reading paired with the wall clock at
// which it was taken, used to derive a rate on the next sample.
type cpuSample struct {
	cpuSeconds float64
	at         time.Time
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
	fraction  float64
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
		percent, cpuErr := strconv.ParseFloat(fields[2], 64)
		if pidErr != nil || parentErr != nil || cpuErr != nil || processID <= 0 || parentPID < 0 || percent < 0 {
			continue
		}
		processes[processID] = darwinCPUProcess{parentPID: parentPID, fraction: percent / 100}
	}
	return processes, len(processes) > 0
}

func darwinCPUFromSnapshot(
	processes map[int]darwinCPUProcess,
	roots []int,
	totalCores float64,
) (float64, bool, float64, bool) {
	if len(processes) == 0 {
		return 0, false, 0, false
	}
	var host float64
	children := map[int][]int{}
	for processID, process := range processes {
		host += process.fraction
		children[process.parentPID] = append(children[process.parentPID], processID)
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
		owned += processes[processID].fraction
	}
	return host, true, clampCores(owned, totalCores), true
}

func newProcSampler() *procSampler {
	return &procSampler{
		last: map[int]cpuSample{},
		tree: map[int]map[int]struct{}{},
	}
}

// CPUUsage dispatches to the platform reading.
func (p *procSampler) CPUUsage(pid int) (ProcUsage, bool) { return p.sample(pid) }

func (p *procSampler) CPUUsages(pids []int) map[int]ProcUsage { return p.sampleMany(pids) }

// collectSubtree returns root and every process reachable from it through
// the parent->children map, so a holder's forked work (make -j, test
// runners, shell pipelines) is credited to the holder even when it runs
// in child process groups the holder never touches. The seen set guards
// against a cycle from recycled pids.
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
