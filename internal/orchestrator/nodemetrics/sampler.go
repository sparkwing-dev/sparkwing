// Package nodemetrics runs an in-process resource sampler. v0 is
// process-wide; concurrent nodes share the signal.
package nodemetrics

import (
	"context"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Sample is one resource reading.
type Sample struct {
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

// Sink absorbs samples.
type Sink interface {
	Push(ctx context.Context, sample Sample) error
}

// CPUAccountingAvailable reports whether this platform can measure a
// process's CPU time, so a caller can tell a healthy sampler's genuine
// near-zero CPU reading (a sleep-heavy pipeline) from a blind sampler's
// uninformative zero. It matches the signal the sampler itself uses to
// decide whether to emit real CPU numbers or announce its blindness.
func CPUAccountingAvailable() bool {
	_, ok := readCPUTime()
	return ok
}

// reportedChildCPU is the cumulative user+system CPU that the per-command
// wait4 path has already attributed to finished SDK commands. RUSAGE_CHILDREN
// counts every reaped child, so the sampler subtracts this to avoid counting
// an SDK command twice; children spawned outside the SDK wrapper leave no
// entry here and so still surface through the RUSAGE_CHILDREN delta.
var reportedChildCPU atomic.Int64

// AddReportedChildCPU records CPU a per-command resource report has already
// accounted for, so the sampler does not re-count the same usage when it
// lands in RUSAGE_CHILDREN at reap. It is process-wide, matching the scope of
// RUSAGE_CHILDREN and the v0 sampler.
func AddReportedChildCPU(d time.Duration) {
	if d > 0 {
		reportedChildCPU.Add(int64(d))
	}
}

// blindOnce guards the single log line emitted when the platform offers
// no CPU accounting, so a blind sampler announces itself instead of
// masquerading as a healthy one reporting genuine zeros.
var blindOnce sync.Once

// Run samples until ctx cancels; blocks. Sink errors are swallowed.
func Run(ctx context.Context, interval time.Duration, sink Sink) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	prevCPU, havePrev := readCPUTime()
	if !havePrev {
		blindOnce.Do(func() {
			log.Printf("nodemetrics: CPU accounting unavailable on %s; CPU samples will be zero", runtime.GOOS)
		})
	}
	prevWall := time.Now()

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			sample := Sample{TS: now, MemoryBytes: readMemoryBytes()}
			if cpu, ok := readCPUTime(); ok && havePrev {
				dWall := now.Sub(prevWall).Seconds()
				if dWall > 0 {
					millicores := int64((cpu - prevCPU).Seconds() / dWall * 1000.0)
					sample.CPUMillicores = max(millicores, 0)
				}
				prevCPU = cpu
				prevWall = now
			}
			_ = sink.Push(ctx, sample)
		}
	}
}

// readMemoryBytes returns process RSS from the platform source, falling
// back to runtime.MemStats.Sys where no per-process RSS is available.
func readMemoryBytes() int64 {
	if rss, ok := processRSS(); ok {
		return rss
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Sys)
}
