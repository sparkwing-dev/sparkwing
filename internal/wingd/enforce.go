package wingd

import (
	"fmt"
	"strconv"
)

// cgroupCPUPeriodUS is the cgroup v2 cpu.max accounting period. A budget
// of N cores becomes a quota of N periods' worth of runtime per period.
const cgroupCPUPeriodUS = 100000

// backgroundNice is the scheduler nice applied to admitted runs under
// macOS enforcement, on top of background QoS. It makes the demotion
// visible to standard tools and adds ordinary scheduler yielding; the
// real throttling comes from the background QoS class.
const backgroundNice = 10

// cgroupLimiter is a daemon-managed cgroup v2 that walls admitted runs to
// the machine budget on Linux. It is nil on other platforms and whenever
// enforcement is off or the cgroup filesystem is unavailable.
type cgroupLimiter struct {
	path string
}

// setupEnforcement prepares OS-level budget hardening. On Linux with an
// enforcing budget it creates a cgroup matching the budget; a cgroupfs
// that is absent or unwritable is logged and left as a soft cap (the
// admission ledger still bounds usage). On macOS enforcement is applied
// per-process at grant time, so there is nothing to set up here.
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

// enforceHolderProcess applies the machine budget's OS-level hardening to
// a newly admitted run process: on Linux it moves the process into the
// budget cgroup; on macOS it demotes the process to background QoS. Both
// are best-effort -- a failure is logged and leaves the admission cap as
// the sole constraint. Runs off the daemon lock.
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

// cpuMaxLine formats a cgroup v2 cpu.max value for a core budget: a quota
// of cores periods per period. A non-positive budget yields "max"
// (uncapped), so a memory-only budget leaves CPU alone.
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

// memMaxLine formats a cgroup v2 memory.max value, or "max" when the
// budget leaves memory uncapped.
func memMaxLine(b uint64) string {
	if b == 0 {
		return "max"
	}
	return strconv.FormatUint(b, 10)
}
