package wingd

import (
	"fmt"
	"strconv"
)

const cgroupCPUPeriodUS = 100000

const backgroundNice = 10

type cgroupLimiter struct {
	//lint:ignore U1000 used by the Linux implementation
	path string
}

func (d *Daemon) setupEnforcement() {
	if !d.cfg.Budget.Enforcing() || !cgroupSupported {
		return
	}
	cg, err := newCgroupLimiter(d.layout.dir, d.budgetCores, d.budgetMemory)
	if err != nil {
		d.cfg.logf("budget enforce: cgroup unavailable, admission cap and per-process limits still apply: %v", err)
		return
	}
	d.cgroup = cg
}

func (d *Daemon) enforceHolderProcess(pid int, runID string) {
	if d.cgroup != nil {
		if err := d.cgroup.join(pid); err != nil {
			d.cfg.logf("budget enforce: cgroup join run %s pid %d: %v", runID, pid, err)
		}
	}
	if err := backgroundProcess(pid); err != nil {
		d.cfg.logf("budget enforce: background run %s pid %d: %v", runID, pid, err)
	}
}

func cpuMaxLine(cores float64) string {
	if cores <= 0 {
		return "max " + strconv.Itoa(cgroupCPUPeriodUS)
	}
	quota := int64(cores * float64(cgroupCPUPeriodUS))
	if quota < 1 {
		quota = 1
	}
	return fmt.Sprintf("%d %d", quota, cgroupCPUPeriodUS)
}

func memMaxLine(b uint64) string {
	if b == 0 {
		return "max"
	}
	return strconv.FormatUint(b, 10)
}
