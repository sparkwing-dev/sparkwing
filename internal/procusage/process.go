// Package procusage samples process-tree CPU rates and resident memory.
package procusage

import (
	"math"
	"math/bits"
	"time"
)

type Identity struct {
	PID   int
	Birth uint64
}

type Process struct {
	Identity
	Parent            int
	ReadAt            time.Time
	CPU               time.Duration
	ReapedCPU         time.Duration
	RSS               int64
	CPUAvailable      bool
	ReapedAvailable   bool
	RSSAvailable      bool
	AncestryAvailable bool
	IdentityAvailable bool
}

type Snapshot struct {
	Start, End time.Time
	Processes  map[int]Process
	Available  bool
}

func ticksDuration(ticks, frequency uint64) (time.Duration, bool) {
	if frequency == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(ticks, uint64(time.Second))
	if hi >= frequency {
		return 0, false
	}
	ns, _ := bits.Div64(hi, lo, frequency)
	if ns > math.MaxInt64 {
		return 0, false
	}
	return time.Duration(ns), true
}
