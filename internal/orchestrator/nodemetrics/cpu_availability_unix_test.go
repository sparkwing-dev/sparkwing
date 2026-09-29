//go:build unix

package nodemetrics

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestCPUReadRequiresBothCounters(t *testing.T) {
	old := getrusage
	defer func() { getrusage = old }()
	for _, failed := range []int{unix.RUSAGE_SELF, unix.RUSAGE_CHILDREN} {
		getrusage = func(who int, r *unix.Rusage) error {
			if who == failed {
				return unix.EIO
			}
			*r = unix.Rusage{}
			return nil
		}
		if got, ok := readCPUTime(); ok || got != 0 {
			t.Errorf("counter %d failed: CPU=%v, available=%t", failed, got, ok)
		}
	}
	getrusage = func(_ int, r *unix.Rusage) error { *r = unix.Rusage{}; return nil }
	if got, ok := readCPUTime(); !ok || got != 0 {
		t.Errorf("zero counters: CPU=%v, available=%t", got, ok)
	}
}
