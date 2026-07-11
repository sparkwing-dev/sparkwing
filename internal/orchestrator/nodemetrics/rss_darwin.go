//go:build darwin

package nodemetrics

import "golang.org/x/sys/unix"

// processRSS returns the peak resident set size via getrusage, whose
// ru_maxrss is reported in bytes on darwin. macOS exposes no cheap
// current-RSS counter without task_info, so the high-water mark stands in;
// the profile takes the peak across samples regardless.
func processRSS() (int64, bool) {
	var ru unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	if ru.Maxrss <= 0 {
		return 0, false
	}
	return int64(ru.Maxrss), true
}
