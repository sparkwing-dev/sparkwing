//go:build !darwin && !linux

package wingd

import "runtime"

func sampleHost() (HostStat, error) {
	return HostStat{TotalCores: float64(runtime.NumCPU())}, nil
}

func (p *procSampler) sample(int) (ProcUsage, bool) { return ProcUsage{}, false }

func (p *procSampler) sampleMany([]int) map[int]ProcUsage { return nil }

func (s *ownedProcSampler) sampleOwned([]int) (float64, bool) { return 0, false }
