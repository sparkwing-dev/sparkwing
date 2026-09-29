//go:build unix

package nodemetrics

import (
	"time"

	"golang.org/x/sys/unix"
)

var getrusage = unix.Getrusage

func readCPUTime() (time.Duration, bool) {
	var self unix.Rusage
	if err := getrusage(unix.RUSAGE_SELF, &self); err != nil {
		return 0, false
	}
	total := time.Duration(self.Utime.Nano()) + time.Duration(self.Stime.Nano())
	var children unix.Rusage
	if err := getrusage(unix.RUSAGE_CHILDREN, &children); err != nil {
		return 0, false
	}
	childCPU := time.Duration(children.Utime.Nano()) + time.Duration(children.Stime.Nano())
	if unattributed := childCPU - time.Duration(reportedChildCPU.Load()); unattributed > 0 {
		total += unattributed
	}
	return total, true
}
