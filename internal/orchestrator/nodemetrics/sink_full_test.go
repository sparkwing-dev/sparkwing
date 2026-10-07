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
	read := fixedReadings(t, reading{start, time.Second, 100, true, nil}, reading{start.Add(2 * time.Second), 2 * time.Second, 300, true, nil})
	full := &fullSink{}
	finish := attach(t.Context(), full, read)
	if _, err := finish(); err != nil {
		t.Fatalf("finish after a full sink = %v, want nil", err)
	}
	if delivery, _ := finish(); delivery != (Delivery{}) {
		t.Fatalf("full sink counted as attempted delivery: %+v", delivery)
	}
	if full.pushes != 1 {
		t.Fatalf("a full sink got %d samples, want one and then none", full.pushes)
	}
}
