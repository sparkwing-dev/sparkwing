//go:build !linux

package wingd

import "errors"

// errCgroupUnsupported reports that kernel cgroup enforcement is a
// Linux-only mechanism; other platforms harden the budget by other means
// (background scheduling on macOS) or not at all.
var errCgroupUnsupported = errors.New("cgroup enforcement is Linux-only")

// cgroupSupported reports that this platform cannot wall runs with a
// cgroup; enforcement here is per-process demotion at grant time.
const cgroupSupported = false

// newCgroupLimiter is unsupported off Linux; the daemon degrades to an
// admission-only cap plus any per-process demotion the platform offers.
func newCgroupLimiter(string, float64, uint64) (*cgroupLimiter, error) {
	return nil, errCgroupUnsupported
}

// join is never reached off Linux because newCgroupLimiter never returns
// a limiter there; it exists so the shared enforcement code compiles.
func (c *cgroupLimiter) join(int) error { return errCgroupUnsupported }
