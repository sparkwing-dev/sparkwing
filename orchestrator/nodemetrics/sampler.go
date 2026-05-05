// Package nodemetrics runs an in-process resource sampler. v0 is
// process-wide; concurrent nodes share the signal.
package nodemetrics

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
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

// Run samples until ctx cancels; blocks. Sink errors are swallowed.
func Run(ctx context.Context, interval time.Duration, sink Sink) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	var (
		prevCPUJiffies int64
		prevWall       time.Time
	)
	// Seed so the first delta is meaningful.
	if jiffies, ok := readCPUJiffies(); ok {
		prevCPUJiffies = jiffies
		prevWall = time.Now()
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			sample := Sample{TS: now, MemoryBytes: readMemoryBytes()}
			if jiffies, ok := readCPUJiffies(); ok && !prevWall.IsZero() {
				dWall := now.Sub(prevWall).Seconds()
				dJiffies := jiffies - prevCPUJiffies
				if dWall > 0 {
					// CLK_TCK=100 on Linux; CPU-second/wall-second*1000 = millicores.
					cpuSeconds := float64(dJiffies) / 100.0
					millicores := int64((cpuSeconds / dWall) * 1000.0)
					sample.CPUMillicores = max(millicores, 0)
				}
				prevCPUJiffies = jiffies
				prevWall = now
			}
			_ = sink.Push(ctx, sample)
		}
	}
}

// readMemoryBytes returns RSS, falling back to runtime.MemStats.Sys.
func readMemoryBytes() int64 {
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			if rssPages, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return rssPages * int64(os.Getpagesize())
			}
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Sys)
}

// readCPUJiffies returns utime+stime from /proc/self/stat (Linux).
func readCPUJiffies() (int64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// Walk past the last ')' so a parenthesized comm with spaces
	// doesn't confuse field indexing.
	s := string(data)
	end := strings.LastIndex(s, ")")
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(s[end+1:])
	// After comm: field 0 = state, utime = field 11, stime = 12.
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseInt(fields[11], 10, 64)
	stime, err2 := strconv.ParseInt(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}
