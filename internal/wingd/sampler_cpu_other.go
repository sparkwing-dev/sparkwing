//go:build !darwin && !linux

package wingd

// cpuTracker is inert outside Linux and macOS, matching the rest of the
// host sampler on those platforms.
type cpuTracker struct{}

// busyCores reports nothing measured, so admission subtracts no external
// cores rather than treating an unread machine as an idle one.
func (c *cpuTracker) busyCores(float64) (float64, bool) { return 0, false }
