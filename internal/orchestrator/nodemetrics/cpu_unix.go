//go:build unix

package nodemetrics

import (
	"time"

	"golang.org/x/sys/unix"
)

// readCPUTime returns this process's cumulative user+system CPU time via
// getrusage, portable across every unix. It sums RUSAGE_SELF with the
// RUSAGE_CHILDREN a raw os/exec child leaves invisible to the SDK's
// per-command path, minus the CPU that path has already attributed, so a
// reaped SDK command is not counted twice. The bool reports success.
func readCPUTime() (time.Duration, bool) {
	var self unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &self); err != nil {
		return 0, false
	}
	total := time.Duration(self.Utime.Nano()) + time.Duration(self.Stime.Nano())
	var children unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_CHILDREN, &children); err == nil {
		childCPU := time.Duration(children.Utime.Nano()) + time.Duration(children.Stime.Nano())
		// safety: reportedChildCPU only ever holds usage already present in
		// RUSAGE_CHILDREN, so this difference is the un-attributed remainder.
		if unattributed := childCPU - time.Duration(reportedChildCPU.Load()); unattributed > 0 {
			total += unattributed
		}
	}
	return total, true
}
