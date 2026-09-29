package nodemetrics

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fullSink struct{ pushes atomic.Int32 }

func (s *fullSink) Push(context.Context, Sample) error {
	s.pushes.Add(1)
	return ErrSinkFull
}

func TestAttach_FullSinkStopsSampling(t *testing.T) {
	stubReaders(t, clampingCPU(), func() int64 { return 1000 })
	t.Cleanup(SetIntervalForTest(5 * time.Millisecond))
	full := &fullSink{}
	detach := Attach(context.Background(), full)
	defer detach()
	waitForSamplerStop(t)
	if n := full.pushes.Load(); n != 1 {
		t.Fatalf("a full sink got %d samples, want one and then none", n)
	}
}
