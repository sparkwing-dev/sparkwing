package wingd

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
