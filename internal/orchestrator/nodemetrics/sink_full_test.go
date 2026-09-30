package nodemetrics

import (
	"context"
	"testing"
	"time"
)

type fullSink struct{ pushes int }

func (s *fullSink) Push(context.Context, Sample) error {
	s.pushes++
	return ErrSinkFull
}

func TestAttach_FullSinkStopsSampling(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{start, time.Second, 100, true, nil}, reading{start.Add(2 * time.Second), 2 * time.Second, 300, true, nil})
	full := &fullSink{}
	finish := Attach(t.Context(), full)
	if err := finish(); err != nil {
		t.Fatalf("finish after a full sink = %v, want nil", err)
	}
	if full.pushes != 1 {
		t.Fatalf("a full sink got %d samples, want one and then none", full.pushes)
	}
}
