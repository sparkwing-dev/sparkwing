//go:build linux

package wingd

import "os"

// cpuTracker derives host CPU utilization from the change in /proc/stat's
// cumulative counters between successive samples. It holds the previous
// reading because a single reading of a cumulative counter describes the
// machine's whole uptime, not its present state.
type cpuTracker struct {
	prev cpuTotals
	seen bool
}

// busyCores reports cores busy since the previous call. The first call
// establishes the baseline and reports nothing measured.
func (c *cpuTracker) busyCores(totalCores float64) (float64, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	cur, ok := parseProcStatCPU(string(data))
	if !ok {
		return 0, false
	}
	prev, seen := c.prev, c.seen
	c.prev, c.seen = cur, true
	if !seen {
		return 0, false
	}
	return busyCoresFromTotals(prev, cur, totalCores)
}
