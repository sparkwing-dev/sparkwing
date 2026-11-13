package lease

import "golang.org/x/sys/unix"

func monotonicClock() int64 {
	// safety: Go's monotonic readings come from CLOCK_MONOTONIC on Linux, so the origin bracket shares their clock.
	var now unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &now) != nil {
		return 0
	}
	return now.Nano()
}
