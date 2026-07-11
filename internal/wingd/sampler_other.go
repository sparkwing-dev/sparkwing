//go:build !darwin && !linux

package wingd

import "runtime"

func sampleHost() (HostStat, error) {
	return HostStat{TotalCores: float64(runtime.NumCPU())}, nil
}
