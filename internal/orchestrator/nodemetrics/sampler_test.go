package nodemetrics

import (
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"
)

type captureSink struct {
	mu          sync.Mutex
	samples     []Sample
	memoryReady chan struct{}
	memoryOnce  sync.Once
	sampleReady chan struct{}
}

func (s *captureSink) Push(_ context.Context, sample Sample) error {
	s.mu.Lock()
	s.samples = append(s.samples, sample)
	if sample.MemoryBytes > 0 && s.memoryReady != nil {
		s.memoryOnce.Do(func() { close(s.memoryReady) })
	}
	s.mu.Unlock()
	if s.sampleReady != nil {
		select {
		case s.sampleReady <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *captureSink) peakCPU() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peak int64
	for _, sm := range s.samples {
		if sm.CPUAvailable && sm.CPUMillicores > peak {
			peak = sm.CPUMillicores
		}
	}
	return peak
}

func TestAttach_ReportsNonzeroCPUUnderLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.6s of real work; the fast class runs under -short")
	}
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detach := Attach(ctx, sink, false)

	burnUntil := time.Now().Add(600 * time.Millisecond)
	x := 0
	for time.Now().Before(burnUntil) {
		x++
		_ = x * x
	}
	detach()
	waitForSamplerStop(t)

	if peak := sink.peakCPU(); peak <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 after burning a core", peak)
	}
}

func TestSampledChildProcess(t *testing.T) {
	if flag.Lookup("test.run").Value.String() != "^TestSampledChildProcess$" {
		t.Skip("subprocess entry point")
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); close(done) }()
	var value uint64 = 1
	for {
		select {
		case <-done:
			runtime.KeepAlive(value)
			return
		default:
			value = value*1664525 + 1013904223
		}
	}
}

func TestAttach_CountsLiveRawExecChildCPU(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native process-tree reader unavailable")
	}
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	sink := &captureSink{sampleReady: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	detach := Attach(ctx, sink, true)
	defer func() { detach(); waitForSamplerStop(t) }()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSampledChildProcess$")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for sink.peakCPU() <= 300 {
		select {
		case <-sink.sampleReady:
		case <-ctx.Done():
			t.Fatalf("live child CPU peak=%d: %v", sink.peakCPU(), ctx.Err())
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if peak := sink.peakCPU(); peak <= 300 {
		t.Fatalf("valid live child CPU peak=%d millicores, want >300", peak)
	}
}

func TestAttach_ReportsMemory(t *testing.T) {
	t.Cleanup(SetIntervalForTest(40 * time.Millisecond))
	memoryReady := make(chan struct{})
	sink := &captureSink{memoryReady: memoryReady}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	detach := Attach(ctx, sink, false)
	t.Cleanup(func() {
		detach()
		cancel()
		waitForSamplerStop(t)
	})
	select {
	case <-memoryReady:
	case <-ctx.Done():
		t.Fatalf("sampler did not report nonzero memory: %v", ctx.Err())
	}
	detach()
	waitForSamplerStop(t)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.samples) == 0 {
		t.Fatal("no samples captured")
	}
	for _, sm := range sink.samples {
		if sm.MemoryBytes > 0 {
			return
		}
	}
	t.Fatal("all memory readings were zero")
}
