//go:build !darwin && !linux && !windows

package wingd

type cpuTracker struct{}

func (c *cpuTracker) busyCores(float64) (float64, bool) { return 0, false }
