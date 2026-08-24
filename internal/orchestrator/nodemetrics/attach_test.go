package nodemetrics

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubReaders points the loop at known readings for the duration of the test.
// The restore waits for the loop to exit so the write cannot race the read.
func stubReaders(t *testing.T, cpu func() (time.Duration, bool), rss func() int64) {
	t.Helper()
	if samplerRunning() {
		t.Fatal("a shared sampler loop is already running; stub before attaching")
	}
	prevCPU, prevRSS := cpuReader, rssReader
	cpuReader, rssReader = cpu, rss
	t.Cleanup(func() {
		waitForSamplerStop(t)
		cpuReader, rssReader = prevCPU, prevRSS
	})
}

// clampingCPU reads far more CPU than any interval can hold, so every tick's
// process total lands on the host-cores clamp and the split is arithmetic a
// test can predict.
func clampingCPU() func() (time.Duration, bool) {
	var cumulative time.Duration
	return func() (time.Duration, bool) {
		cumulative += time.Hour
		return cumulative, true
	}
}

func samplerRunning() bool {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	return shared.stop != nil
}

// waitForSamplerStop blocks until every loop goroutine has exited, retired
// generations included -- a loop that no longer owns the registry is still
// reading the package-level readers until it returns.
func waitForSamplerStop(t *testing.T) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		loopsRunning.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("a shared sampler loop did not exit")
	}
}

func (s *captureSink) snapshot() []Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sample(nil), s.samples...)
}

// awaitSampleAfter waits for a sample the sink took after boundary.
func awaitSampleAfter(t *testing.T, sink *captureSink, boundary time.Time) Sample {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, sm := range sink.snapshot() {
			if sm.TS.After(boundary) {
				return sm
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no sample after %s", boundary)
	return Sample{}
}

// awaitSharedTick waits for one tick both sinks were attached for, which is
// the only tick whose division across both is decided.
func awaitSharedTick(t *testing.T, a, b *captureSink) (Sample, Sample) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		as, bs := a.snapshot(), b.snapshot()
		for _, x := range as {
			for _, y := range bs {
				if x.TS.Equal(y.TS) {
					return x, y
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no interval was sampled with both nodes attached")
	return Sample{}, Sample{}
}

// TestAttach_SplitsIntervalAcrossAttachedNodes pins the property admission
// depends on: two nodes running in the same interval are charged half the
// process each, summing to the process total instead of double-counting it,
// and the survivor takes the whole reading once its neighbor leaves.
func TestAttach_SplitsIntervalAcrossAttachedNodes(t *testing.T) {
	const processRSSBytes = 1000
	hostMilli := int64(runtime.NumCPU()) * 1000
	stubReaders(t, clampingCPU(), func() int64 { return processRSSBytes })
	t.Cleanup(SetIntervalForTest(20 * time.Millisecond))

	first, second := &captureSink{}, &captureSink{}
	ctx := context.Background()
	detachFirst := Attach(ctx, first)
	defer detachFirst()
	detachSecond := Attach(ctx, second)

	a, b := awaitSharedTick(t, first, second)
	if a.CPUMillicores != hostMilli/2 || b.CPUMillicores != hostMilli/2 {
		t.Errorf("shared-interval CPU = %d and %d, want %d each", a.CPUMillicores, b.CPUMillicores, hostMilli/2)
	}
	if sum := a.CPUMillicores + b.CPUMillicores; sum < hostMilli-1 || sum > hostMilli {
		t.Errorf("shared-interval CPU sums to %d, want the process total %d (less integer-division remainder)", sum, hostMilli)
	}
	if a.MemoryBytes != processRSSBytes/2 || b.MemoryBytes != processRSSBytes/2 {
		t.Errorf("shared-interval memory = %d and %d, want %d each", a.MemoryBytes, b.MemoryBytes, processRSSBytes/2)
	}
	if sum := a.MemoryBytes + b.MemoryBytes; sum < processRSSBytes-1 || sum > processRSSBytes {
		t.Errorf("shared-interval memory sums to %d, want the process total %d", sum, processRSSBytes)
	}

	detachSecond()
	alone := awaitSampleAfter(t, first, time.Now())
	if alone.CPUMillicores != hostMilli {
		t.Errorf("sole-node CPU = %d, want the whole process %d", alone.CPUMillicores, hostMilli)
	}
	if alone.MemoryBytes != processRSSBytes {
		t.Errorf("sole-node memory = %d, want the whole process %d", alone.MemoryBytes, processRSSBytes)
	}
}

// TestAttach_LoopStopsWithLastNodeAndRestarts pins the lifecycle: an idle
// process carries no sampling goroutine, a later node starts one again, and a
// cancelled node counts as gone whether or not its detach ran.
func TestAttach_LoopStopsWithLastNodeAndRestarts(t *testing.T) {
	stubReaders(t, clampingCPU(), func() int64 { return 1000 })
	t.Cleanup(SetIntervalForTest(5 * time.Millisecond))

	first := &captureSink{}
	detach := Attach(context.Background(), first)
	awaitSampleAfter(t, first, time.Time{})
	if !samplerRunning() {
		t.Fatal("no loop running while a node is attached")
	}
	detach()
	waitForSamplerStop(t)
	if samplerRunning() {
		t.Fatal("loop still registered after the last node detached")
	}

	second := &captureSink{}
	cancelCtx, cancel := context.WithCancel(context.Background())
	detachSecond := Attach(cancelCtx, second)
	defer detachSecond()
	awaitSampleAfter(t, second, time.Time{})
	if !samplerRunning() {
		t.Fatal("attaching again did not restart the loop")
	}

	cancel()
	waitForSamplerStop(t)
	if samplerRunning() {
		t.Fatal("loop still registered after its only node was cancelled")
	}
}

// TestAttach_RetiredLoopNeverChargesTheLoopThatReplacedIt forces the window a
// stop and an immediate re-attach open: the retired loop is inside a tick when
// the registry changes hands. Its reading belongs to an interval the new node
// did not run in, and its stop channel is already closed, so charging the new
// registry would both double-count the interval and close that channel twice.
// Only the retired loop's tick is given a CPU delta, so any CPU charged to the
// replacement's sink came from the generation that retired.
func TestAttach_RetiredLoopNeverChargesTheLoopThatReplacedIt(t *testing.T) {
	inTick := make(chan struct{})
	release := make(chan struct{})
	var calls, resumed atomic.Int64
	stubReaders(t, func() (time.Duration, bool) {
		if calls.Add(1) == 2 {
			close(inTick)
			<-release
			resumed.Add(1)
			return time.Hour, true
		}
		return 0, true
	}, func() int64 { return 1000 })
	t.Cleanup(SetIntervalForTest(5 * time.Millisecond))

	retiring := &captureSink{}
	detachRetiring := Attach(context.Background(), retiring)
	select {
	case <-inTick:
	case <-time.After(2 * time.Second):
		t.Fatal("first loop never reached a tick")
	}
	detachRetiring()

	replacement := &captureSink{}
	detachReplacement := Attach(context.Background(), replacement)
	defer detachReplacement()
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for resumed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if resumed.Load() == 0 {
		t.Fatal("retired loop never resumed inside its tick")
	}

	awaitSampleAfter(t, replacement, time.Time{})
	settle := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(settle) {
		for _, sm := range replacement.snapshot() {
			if sm.CPUMillicores != 0 {
				t.Fatalf("replacement node charged %d millicores by the retired loop", sm.CPUMillicores)
			}
		}
		time.Sleep(2 * time.Millisecond)
	}

	detachReplacement()
	waitForSamplerStop(t)
	if samplerRunning() {
		t.Fatal("a loop still owns the registry after every node left")
	}
}

// TestAttach_ConcurrentAttachDetachWhileTicking exercises the registry against
// a ticking loop, where -race is the assertion; it also pins that a burst of
// nodes leaving by either route still lands the process back at idle.
func TestAttach_ConcurrentAttachDetachWhileTicking(t *testing.T) {
	stubReaders(t, clampingCPU(), func() int64 { return 1000 })
	t.Cleanup(SetIntervalForTest(time.Millisecond))

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < 25; round++ {
				ctx, cancel := context.WithCancel(context.Background())
				detach := Attach(ctx, &captureSink{})
				time.Sleep(time.Millisecond)
				if (worker+round)%2 == 0 {
					cancel()
					continue
				}
				detach()
				cancel()
			}
		}(worker)
	}
	wg.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for samplerRunning() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if samplerRunning() {
		t.Fatal("loop still running after every node left")
	}
	waitForSamplerStop(t)
}
