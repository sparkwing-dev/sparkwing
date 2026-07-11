//go:build !unix

package nodemetrics

import "time"

// readCPUTime reports not-sampled on platforms without getrusage; the
// sampler logs its blindness once rather than emitting silent zeros.
func readCPUTime() (time.Duration, bool) { return 0, false }
