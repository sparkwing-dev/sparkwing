//go:build unix

package nodemetrics

import (
	"time"

	"golang.org/x/sys/unix"
)

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
