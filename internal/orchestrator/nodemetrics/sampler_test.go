package nodemetrics

import (
	"context"
	"sync"
	"testing"
	"time"
)

type captureSink struct {
	mu      sync.Mutex
	samples []Sample
}

func (s *captureSink) Push(_ context.Context, sample Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, sample)
	return nil
}

func (s *captureSink) peakCPU() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var peak int64
	for _, sm := range s.samples {
		if sm.CPUMillicores > peak {
			peak = sm.CPUMillicores
		}
	}
	return peak
}

// TestRun_ReportsNonzeroCPUUnderLoad pins BW-651: on the host platform a
// CPU-burning process must produce a nonzero sampled peak, so learned
// capacity can activate rather than costing every run by the default.
func TestRun_ReportsNonzeroCPUUnderLoad(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 40*time.Millisecond, sink)
		close(done)
	}()

	burnUntil := time.Now().Add(600 * time.Millisecond)
	x := 0
	for time.Now().Before(burnUntil) {
		x++
		_ = x * x
	}
	cancel()
	<-done

	if peak := sink.peakCPU(); peak <= 0 {
		t.Fatalf("peak CPU millicores = %d, want > 0 after burning a core", peak)
	}
}

// TestRun_ReportsMemory asserts the sampler reports a nonzero memory
// reading from the platform RSS source or its runtime fallback.
func TestRun_ReportsMemory(t *testing.T) {
	sink := &captureSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, 40*time.Millisecond, sink)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

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
