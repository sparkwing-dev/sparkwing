//go:build !darwin && !linux

package wingd

import "runtime"

// sampleHost reports the core count and nothing else: platforms outside
// Linux and macOS have no host-pressure reading here, so both dimensions
// stay unmeasured and the queue view says so rather than showing an idle
// machine.
func sampleHost() (HostStat, error) {
	return HostStat{TotalCores: float64(runtime.NumCPU())}, nil
}

// sample reports not-sampled: platforms outside Linux and macOS offer no
// cheap per-process CPU reading, so stall flagging stays inert here
// rather than pulling in a heavier dependency.
func (p *procSampler) sample(int) (ProcUsage, bool) { return ProcUsage{}, false }

func (p *procSampler) sampleMany([]int) map[int]ProcUsage { return nil }
