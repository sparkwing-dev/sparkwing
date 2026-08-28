package nodemetrics

import (
	"context"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type Sample struct {
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

type Sink interface {
	Push(ctx context.Context, sample Sample) error
}

const defaultInterval = 2 * time.Second

var intervalNanos atomic.Int64

func SetIntervalForTest(d time.Duration) (restore func()) {
	previous := intervalNanos.Swap(int64(d))
	return func() { intervalNanos.Store(previous) }
}

func Interval() time.Duration {
	if d := time.Duration(intervalNanos.Load()); d > 0 {
		return d
	}
	return defaultInterval
}

var (
	cpuReader = readCPUTime
	rssReader = readMemoryBytes
)

func CPUAccountingAvailable() bool {
	_, ok := readCPUTime()
	return ok
}

var reportedChildCPU atomic.Int64

func AddReportedChildCPU(d time.Duration) {
	if d > 0 {
		reportedChildCPU.Add(int64(d))
	}
}

var blindOnce sync.Once

type attachment struct {
	ctx  context.Context
	sink Sink
}

type sharedSampler struct {
	mu    sync.Mutex
	sinks map[*attachment]struct{}
	stop  chan struct{}
}

var shared = &sharedSampler{sinks: make(map[*attachment]struct{})}

var loopsRunning sync.WaitGroup

func Attach(ctx context.Context, sink Sink) (detach func()) {
	a := &attachment{ctx: ctx, sink: sink}
	shared.add(a)
	return func() { shared.remove(a) }
}

func (s *sharedSampler) add(a *attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinks[a] = struct{}{}
	if s.stop == nil {
		stop := make(chan struct{})
		s.stop = stop
		loopsRunning.Add(1)
		go s.loop(stop, Interval())
	}
}

func (s *sharedSampler) remove(a *attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sinks, a)
	if len(s.sinks) == 0 && s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

func (s *sharedSampler) liveSinks(stop chan struct{}) []*attachment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != stop {
		return nil
	}
	for a := range s.sinks {
		if a.ctx.Err() != nil {
			delete(s.sinks, a)
		}
	}
	if len(s.sinks) == 0 {
		close(stop)
		s.stop = nil
		return nil
	}
	live := make([]*attachment, 0, len(s.sinks))
	for a := range s.sinks {
		live = append(live, a)
	}
	return live
}

func (s *sharedSampler) loop(stop chan struct{}, interval time.Duration) {
	defer loopsRunning.Done()
	prevCPU, havePrev := cpuReader()
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
		case <-stop:
			return
		case now := <-t.C:
			var totalCPU int64
			if cpu, ok := cpuReader(); ok && havePrev {
				totalCPU = intervalMillicores(cpu-prevCPU, now.Sub(prevWall))
				prevCPU = cpu
				prevWall = now
			}
			live := s.liveSinks(stop)
			if len(live) == 0 {
				return
			}
			// perf: RSS costs a subprocess on darwin, so it is read only once
			// the tick is known to have somewhere to go.
			totalRSS := rssReader()
			// safety: the process total is clamped before it is divided, so
			// the shares sum to a rate the host could serve rather than to a
			// multiple of it.
			share := int64(len(live))
			sample := Sample{
				TS:            now,
				CPUMillicores: totalCPU / share,
				MemoryBytes:   totalRSS / share,
			}
			for _, a := range live {
				_ = a.sink.Push(a.ctx, sample)
			}
		}
	}
}

func intervalMillicores(cpu, wall time.Duration) int64 {
	if wall <= 0 {
		return 0
	}
	millicores := int64(cpu.Seconds() / wall.Seconds() * 1000.0)
	if millicores < 0 {
		return 0
	}
	if hostMilli := int64(runtime.NumCPU()) * 1000; millicores > hostMilli {
		return hostMilli
	}
	return millicores
}

func readMemoryBytes() int64 {
	if rss, ok := processRSS(); ok {
		return rss
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.Sys)
}
