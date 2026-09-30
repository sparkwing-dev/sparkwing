package nodemetrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func fixedReadings(t *testing.T, values ...reading) {
	t.Helper()
	original := usageReader
	index := 0
	usageReader = func() reading {
		value := values[min(index, len(values)-1)]
		index++
		return value
	}
	t.Cleanup(func() { usageReader = original })
	t.Cleanup(SetIntervalForTest(time.Hour))
}

func TestAttachFinalReadingAndIdempotentFinish(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{start, time.Second, 100, true, nil}, reading{start.Add(2 * time.Second), 2 * time.Second, 300, true, nil})
	sink := &captureSink{}
	finish := Attach(t.Context(), sink)
	for range 2 {
		if err := finish(); err != nil {
			t.Fatal(err)
		}
	}
	samples := sink.snapshot()
	if len(samples) != 1 || !samples[0].Valid || samples[0].CPUMillicores != 500 || samples[0].MemoryBytes != 300 || !samples[0].TS.Equal(start.Add(2*time.Second)) {
		t.Fatalf("final reading = %+v; want one valid 500m/300-byte reading at102s", samples)
	}
}

func TestAttachFinishDrainsDeliveryAfterCancellation(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{start, 0, 100, true, nil}, reading{start.Add(time.Second), time.Second, 200, true, nil})
	ctx, cancel := context.WithCancel(t.Context())
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	sink := sinkFunc(func(writeCtx context.Context, _ Sample) error {
		if err := writeCtx.Err(); err != nil {
			t.Errorf("delivery inherited execution cancellation: %v", err)
		}
		if _, ok := writeCtx.Deadline(); !ok {
			t.Error("delivery context has no deadline")
		}
		close(entered)
		<-release
		return nil
	})
	finish := Attach(ctx, sink)
	cancel()
	go func() { finished <- finish() }()
	<-entered
	select {
	case <-finished:
		t.Error("finish returned before delivery completed")
	default:
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestAttachRetainsFirstDeliveryError(t *testing.T) {
	first, second := errors.New("initial write"), errors.New("final write")
	start := time.Unix(100, 0)
	fixedReadings(t, reading{at: start}, reading{start.Add(time.Second), 0, 100, true, nil})
	writes := 0
	finish := Attach(t.Context(), sinkFunc(func(context.Context, Sample) error {
		writes++
		if writes == 1 {
			return first
		}
		return second
	}))
	if err := finish(); !errors.Is(err, first) {
		t.Fatalf("finish error=%v; want first write failure", err)
	}
	if writes != 2 {
		t.Fatalf("writes=%d; want initial unknown and final reading", writes)
	}
}

func TestAttachConcurrentFinishWritesOnce(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{start, 0, 100, true, nil}, reading{start.Add(time.Second), 0, 100, true, nil})
	sink := &captureSink{}
	finish := Attach(t.Context(), sink)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if err := finish(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if got := len(sink.snapshot()); got != 1 {
		t.Fatalf("final writes=%d; want1", got)
	}
}

func TestAttachFinalDescendantsRemainUnknown(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{at: start, valid: true}, reading{at: start.Add(time.Second), cpu: time.Second, memory: 200, valid: true, processes: map[int]processSample{1: {}, 2: {parent: 1}}})
	sink := &captureSink{}
	finish := Attach(t.Context(), sink)
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := sink.snapshot(); len(got) != 1 || got[0].Valid || got[0].MemoryBytes != 200 {
		t.Fatalf("unfinished process tree qualified: %+v", got)
	}
}

func TestAttachRetainsInitialMemoryObservation(t *testing.T) {
	start := time.Unix(100, 0)
	fixedReadings(t, reading{start, time.Second, 300, true, nil}, reading{start.Add(2 * time.Second), 2 * time.Second, 100, true, nil})
	sink := &captureSink{}
	finish := Attach(t.Context(), sink)
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	samples := sink.snapshot()
	if len(samples) != 1 || !samples[0].Valid || samples[0].CPUMillicores != 500 || samples[0].MemoryBytes != 300 {
		t.Fatalf("initial memory observation lost: %+v; want one valid 500m/300-byte interval", samples)
	}
}

func TestAttachDisappearingProcessRemainsUnknown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Unix(100, 0)
		fixedReadings(t,
			reading{at: start, cpu: 1200 * time.Millisecond, memory: 300, valid: true, processes: map[int]processSample{
				1: {birth: 1, cpu: time.Second}, 2: {parent: 1, birth: 2, cpu: 100 * time.Millisecond}, 3: {parent: 1, birth: 3, cpu: 100 * time.Millisecond},
			}},
			reading{at: start.Add(time.Second), cpu: 1400 * time.Millisecond, memory: 100, valid: true, processes: map[int]processSample{
				1: {birth: 1, cpu: 1300 * time.Millisecond, reaped: 100 * time.Millisecond},
			}},
			reading{at: start.Add(2 * time.Second), cpu: 1500 * time.Millisecond, memory: 100, valid: true, processes: map[int]processSample{
				1: {birth: 1, cpu: 1400 * time.Millisecond, reaped: 100 * time.Millisecond},
			}},
		)
		t.Cleanup(SetIntervalForTest(time.Millisecond))
		samples := make(chan Sample, 32)
		finish := Attach(t.Context(), sinkFunc(func(_ context.Context, s Sample) error {
			select {
			case samples <- s:
			default:
			}
			return nil
		}))
		defer func() {
			if err := finish(); err != nil {
				t.Error(err)
			}
		}()
		for i := 0; i < 2; i++ {
			select {
			case sample := <-samples:
				if sample.Valid {
					t.Errorf("disappearing child qualified despite ambiguous reap: %+v", sample)
				}
			case <-time.After(time.Second):
				t.Fatal("sample deadline")
			}
		}
	})
}
