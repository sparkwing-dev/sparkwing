//go:build !darwin && !linux

package wingd

import "runtime"

func sampleHost() (HostStat, error) {
	return HostStat{TotalCores: float64(runtime.NumCPU())}, nil
}

// sample reports not-sampled: platforms outside Linux and macOS offer no
// cheap per-process CPU reading, so stall flagging stays inert here
// rather than pulling in a heavier dependency.
func (p *procSampler) sample(int) (float64, bool) { return 0, false }
