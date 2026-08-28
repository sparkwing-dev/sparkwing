//go:build !linux

package wingd

import "errors"

var errCgroupUnsupported = errors.New("cgroup enforcement is Linux-only")

const cgroupSupported = false

func newCgroupLimiter(string, float64, uint64) (*cgroupLimiter, error) {
	return nil, errCgroupUnsupported
}

func (c *cgroupLimiter) join(int) error { return errCgroupUnsupported }
