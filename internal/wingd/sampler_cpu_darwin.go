//go:build darwin

package wingd

import (
	"context"
	"os/exec"
	"time"
)

// cpuTracker reads host CPU utilization from the process table. It holds
// no state: ps reports a live percentage rather than a cumulative counter,
// so each reading stands alone.
//
// hack: macOS exposes no cumulative per-core tick counter through sysctl,
// and kinfo_proc's own percentage field is unmaintained, so the figure
// comes from ps for the same reason the per-holder sampler's does.
type cpuTracker struct{}

// busyCores sums every process's CPU percentage into a core count.
func (c *cpuTracker) busyCores(totalCores float64) (float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-Ao", "pcpu=").Output()
	if err != nil {
		return 0, false
	}
	return sumProcessCPUPercent(string(out), totalCores)
}
