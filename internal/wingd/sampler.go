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

// ProcSampler reads a single process's recent CPU usage as a fraction of
// one core (1.0 means one core fully busy). The daemon consults it at a
// slow cadence, and only while runs are queued, to tell a working holder
// from one that is alive but wedged. Tests supply a fake so stall
// flagging is exercised deterministically.
type ProcSampler interface {
	// CPUFraction reports the process's CPU usage as a fraction of one
	// core, and false when the process cannot be sampled -- it is gone,
	// or the platform offers no cheap per-process reading.
	CPUFraction(pid int) (float64, bool)
}

// procSampler is the platform ProcSampler. It carries a small per-pid
// memory of the previous cumulative-CPU reading for platforms that
// derive a rate from two samples (Linux); platforms that expose a live
// percentage (macOS) ignore it.
type procSampler struct {
	mu   sync.Mutex
	last map[int]cpuSample
}

// cpuSample is one cumulative-CPU reading paired with the wall clock at
// which it was taken, used to derive a rate on the next sample.
type cpuSample struct {
	cpuSeconds float64
	at         time.Time
}

func newProcSampler() *procSampler {
	return &procSampler{last: map[int]cpuSample{}}
}

// CPUFraction dispatches to the platform reading.
func (p *procSampler) CPUFraction(pid int) (float64, bool) { return p.sample(pid) }
