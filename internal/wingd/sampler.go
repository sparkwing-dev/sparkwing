package wingd

import (
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
	// LoadAverage is the 1-minute run-queue load average, an estimate of
	// how many cores' worth of work is currently demanded.
	LoadAverage float64
	// FreeMemoryBytes is memory the OS reports as available for new
	// allocations.
	FreeMemoryBytes uint64
}

// HostSampler reads the machine's capacity and live pressure. The daemon
// samples it at start (for fixed totals) and periodically (for load and
// free memory), feeding the result into the ledger's headroom. Tests
// supply a fake so admission gating is exercised without touching the
// real machine.
type HostSampler interface {
	Sample() (HostStat, error)
}

// platformSampler reads real host metrics for the current OS.
type platformSampler struct{}

// Sample returns a live [HostStat] for the host it runs on.
func (platformSampler) Sample() (HostStat, error) { return sampleHost() }

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
