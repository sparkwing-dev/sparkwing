//go:build unix

package nodemetrics

import (
	"time"

	"golang.org/x/sys/unix"
)

// readCPUTime returns this process's cumulative user+system CPU time via
// getrusage(RUSAGE_SELF), which is portable across every unix. The bool
// reports whether the reading succeeded.
func readCPUTime() (time.Duration, bool) {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	total := time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
	return total, true
}
