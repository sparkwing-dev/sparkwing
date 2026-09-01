//go:build linux

package wingd

import "os"

type cpuTracker struct {
	prev cpuTotals
	seen bool
}

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
