package nodemetrics

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
