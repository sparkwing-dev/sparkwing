// Package nodemetrics runs an in-process resource sampler. v0 is
// process-wide; concurrent nodes share the signal.
package nodemetrics

import (
	"context"
	"log"
	"runtime"
	"sync"
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
