package nodemetrics

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procusage"
)

type Sample struct {
	TS              time.Time
	CPUMillicores   int64
	MemoryBytes     int64
	CPUAvailable    bool
	MemoryAvailable bool
	Estimated       bool
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

var snapshotReader = (*procusage.Tree).Read

type attachment struct {
	exclusive bool
	ctx       context.Context
	sink      Sink
}

type sharedSampler struct {
	mu    sync.Mutex
	sinks map[*attachment]struct{}
	stop  chan struct{}
}

var shared = &sharedSampler{sinks: make(map[*attachment]struct{})}

var loopsRunning sync.WaitGroup

func Attach(ctx context.Context, sink Sink, exclusive bool) (detach func()) {
	a := &attachment{ctx: ctx, sink: sink, exclusive: exclusive}
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
	tree := procusage.Tree{PID: os.Getpid()}
	tree.Observe(snapshotReader(&tree))

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			reading := tree.Observe(snapshotReader(&tree))
			live := s.liveSinks(stop)
			if len(live) == 0 {
				return
			}
			for _, a := range live {
				share := int64(1)
				if !a.exclusive {
					share = int64(len(live))
				}
				sample := Sample{
					TS:              reading.End,
					CPUMillicores:   min(reading.CPUMillicores, int64(runtime.NumCPU())*1000) / share,
					MemoryBytes:     reading.RSSBytes / share,
					CPUAvailable:    reading.CPUQuality == "sampled",
					MemoryAvailable: reading.MemoryQuality == "sampled",
					Estimated:       !a.exclusive,
				}
				_ = a.sink.Push(a.ctx, sample)
			}
		}
	}
}
