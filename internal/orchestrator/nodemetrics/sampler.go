// Package nodemetrics runs one in-process resource sampler shared by every
// running node. Each interval's process-wide reading is split evenly across
// the nodes attached for that interval: a per-node value is an estimate, but
// the values sum to the process's real usage, which is the property admission
// needs -- charging every node of a parallel fan-out the whole process reads
// as N times the cost the machine actually paid. A node joins and leaves on
// tick boundaries, so up to one interval of its cost can smear onto the nodes
// beside it.
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

// defaultInterval is the cadence the shared loop samples at.
const defaultInterval = 2 * time.Second

// intervalNanos overrides defaultInterval when positive.
var intervalNanos atomic.Int64

// SetIntervalForTest drives the shared loop faster than the default and
// returns a func restoring the previous cadence. The cadence is process-wide
// because the loop is, and a loop reads it once at start, so a test sets it
// before attaching. Nothing in production calls this; it is exported because
// the sampler's callers live in other packages and cannot wait real seconds
// for a tick.
func SetIntervalForTest(d time.Duration) (restore func()) {
	previous := intervalNanos.Swap(int64(d))
	return func() { intervalNanos.Store(previous) }
}

// currentInterval is the cadence the next loop will start with.
func currentInterval() time.Duration {
	if d := time.Duration(intervalNanos.Load()); d > 0 {
		return d
	}
	return defaultInterval
}

// cpuReader and rssReader stand between the loop and the platform so a test
// can drive the split arithmetic from known readings; against a live process
// whose true usage nothing can pin down, the arithmetic is unobservable.
var (
	cpuReader = readCPUTime
	rssReader = readMemoryBytes
)

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
// RUSAGE_CHILDREN and of the shared sampler.
func AddReportedChildCPU(d time.Duration) {
	if d > 0 {
		reportedChildCPU.Add(int64(d))
	}
}

// blindOnce guards the single log line emitted when the platform offers
// no CPU accounting, so a blind sampler announces itself instead of
// masquerading as a healthy one reporting genuine zeros.
var blindOnce sync.Once

// attachment is one node's claim on the shared sampler. It carries the node's
// context so a store write inherits the node's cancellation.
type attachment struct {
	ctx  context.Context
	sink Sink
}

// sharedSampler owns the process's one sampling loop and the set of nodes it
// divides each reading among. stop is non-nil exactly while a loop owns the
// registry, so it doubles as the running flag and as that loop's identity.
type sharedSampler struct {
	mu    sync.Mutex
	sinks map[*attachment]struct{}
	stop  chan struct{}
}

var shared = &sharedSampler{sinks: make(map[*attachment]struct{})}

// loopsRunning counts live sampling goroutines, a retired generation still
// winding down included. Owning the registry and still executing are separate
// states, so a test that injects package-level readers has no other way to
// know every loop has stopped reading them.
var loopsRunning sync.WaitGroup

// Attach gives sink a share of every interval it is attached for and returns
// a func that ends its participation. The first attachment starts the shared
// loop and the last one to leave stops it, so an idle process carries no
// sampling goroutine. Samples are pushed with ctx, and a node whose ctx is
// cancelled stops taking a share whether or not it detached.
func Attach(ctx context.Context, sink Sink) (detach func()) {
	a := &attachment{ctx: ctx, sink: sink}
	shared.add(a)
	return func() { shared.remove(a) }
}

// add registers an attachment, starting a loop if none is running.
func (s *sharedSampler) add(a *attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sinks[a] = struct{}{}
	if s.stop == nil {
		stop := make(chan struct{})
		s.stop = stop
		loopsRunning.Add(1)
		go s.loop(stop, currentInterval())
	}
}

// remove drops an attachment and ends the loop with the last one.
func (s *sharedSampler) remove(a *attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sinks, a)
	if len(s.sinks) == 0 && s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

// liveSinks returns the attachments a tick divides its reading among, first
// dropping any whose node has been cancelled: a node that ended between ticks
// must not be charged for an interval it did not run in. An empty result
// means the calling loop is finished -- either the registry emptied or a
// newer loop already owns it.
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

// loop samples until stop closes or the last attachment goes away. It carries
// the previous CPU reading, so only one loop may run at a time: two would each
// subtract from their own baseline and charge the same CPU twice.
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

// intervalMillicores derives an interval's average CPU draw in millicores
// from the CPU consumed and the wall time it spanned, clamped to the host's
// core count. The clamp is load-bearing: a reaped subtree's cumulative CPU
// (a long `make -j`) becomes visible to RUSAGE_CHILDREN all at once, so
// dividing it by a single short interval reads as a rate no physical machine
// could sustain; capping at host cores keeps that artifact from being stored
// as a peak far above real concurrency. A non-positive interval draws nothing.
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
