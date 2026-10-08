package nodemetrics_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/nodemetrics"
)

type deliverySink func(context.Context, nodemetrics.Sample) error

func (sink deliverySink) Push(ctx context.Context, sample nodemetrics.Sample) error {
	return sink(ctx, sample)
}

func TestDeliveryLossKeepsSamplingAndReportsFirstLoss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Cleanup(nodemetrics.SetIntervalForTest(time.Millisecond))
		first := errors.New("sample rejected")
		calls := 0
		delivered := make(chan struct{}, 10)
		finish := nodemetrics.Attach(t.Context(), deliverySink(func(ctx context.Context, _ nodemetrics.Sample) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("push has no deadline")
			}
			calls++
			delivered <- struct{}{}
			if calls == 1 {
				return first
			}
			return nil
		}))
		for range 3 {
			<-delivered
		}
		if delivery, err := finish(); !errors.Is(err, first) {
			t.Fatalf("finish=%v; want first loss", err)
		} else if delivery.Attempted != int64(calls) || delivery.Lost != 1 {
			t.Fatalf("delivery=%+v; want %d attempted and one lost", delivery, calls)
		}
		if calls < 4 {
			t.Fatalf("deliveries=%d; want later and final samples", calls)
		}
	})
}

func TestLostIntervalPreservesCPUAndInitialMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Cleanup(nodemetrics.SetIntervalForTest(time.Second))
		start := time.Unix(100, 0)
		first := errors.New("sample rejected")
		samples := make(chan nodemetrics.Sample, 4)
		calls := 0
		finish := nodemetrics.AttachReadingsForTest(t.Context(), deliverySink(func(_ context.Context, sample nodemetrics.Sample) error {
			calls++
			samples <- sample
			if calls == 1 {
				return first
			}
			return nil
		}),
			nodemetrics.ReadingForTest{At: start, Memory: 300},
			nodemetrics.ReadingForTest{At: start.Add(time.Second), CPU: time.Second, Memory: 100},
			nodemetrics.ReadingForTest{At: start.Add(3 * time.Second), CPU: 1500 * time.Millisecond, Memory: 100},
			nodemetrics.ReadingForTest{At: start.Add(4 * time.Second), CPU: 2 * time.Second, Memory: 100},
		)
		<-samples
		recovered := <-samples
		if !recovered.Valid || recovered.CPUMillicores != 500 || recovered.MemoryBytes != 300 {
			t.Fatalf("recovered sample=%+v; want 1500ms CPU across 3s and retained initial memory", recovered)
		}
		for range 2 {
			delivery, err := finish()
			if delivery != (nodemetrics.Delivery{Attempted: 3, Lost: 1}) || !errors.Is(err, first) {
				t.Fatalf("finish=(%+v,%v); want 3 attempted, 1 lost and first loss", delivery, err)
			}
		}
		final := <-samples
		cpu := recovered.CPUMillicores*3 + final.CPUMillicores
		if cpu != 2000 {
			t.Fatalf("recorded CPU=%d milliseconds; want 2000", cpu)
		}
	})
}

func TestStalledDeliveryTimesOutAndSamplingContinues(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Cleanup(nodemetrics.SetIntervalForTest(time.Second))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		calls := 0
		delivered := make(chan struct{}, 4)
		finish := nodemetrics.AttachReadingsForTest(ctx, deliverySink(func(writeCtx context.Context, _ nodemetrics.Sample) error {
			calls++
			if writeCtx.Err() != nil {
				t.Errorf("sample inherited cancellation: %v", writeCtx.Err())
			}
			if calls == 1 {
				<-writeCtx.Done()
				return writeCtx.Err()
			}
			delivered <- struct{}{}
			return nil
		}), nodemetrics.ReadingForTest{At: time.Unix(100, 0)})
		<-delivered
		delivery, err := finish()
		if delivery.Lost != 1 || delivery.Attempted < 3 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("finish=(%+v,%v); want timeout loss followed by later and final deliveries", delivery, err)
		}
	})
}

func TestLostInvalidSamplesAreCountedAsLostExclusions(t *testing.T) {
	t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
	start := time.Unix(1_700_000_000, 0)
	finish := nodemetrics.AttachReadingsForTest(t.Context(), deliverySink(func(context.Context, nodemetrics.Sample) error {
		return errors.New("sample rejected")
	}), nodemetrics.ReadingForTest{At: start, Invalid: true}, nodemetrics.ReadingForTest{At: start.Add(time.Second), CPU: time.Second, Memory: 1})
	delivery, err := finish()
	if err == nil || delivery.Lost != 2 || delivery.LostInvalid != 2 {
		t.Fatalf("delivery=%+v err=%v, want both invalid samples lost", delivery, err)
	}
}

func TestFullSinkStillReportsALostInvalidSample(t *testing.T) {
	t.Cleanup(nodemetrics.SetIntervalForTest(time.Hour))
	start := time.Unix(1_700_000_000, 0)
	finish := nodemetrics.AttachReadingsForTest(t.Context(), deliverySink(func(context.Context, nodemetrics.Sample) error {
		return nodemetrics.ErrSinkFull
	}), nodemetrics.ReadingForTest{At: start, Invalid: true}, nodemetrics.ReadingForTest{At: start.Add(time.Second), CPU: time.Second, Memory: 1})
	delivery, err := finish()
	if !errors.Is(err, nodemetrics.ErrSinkFull) || delivery.Lost != 0 || delivery.LostInvalid == 0 {
		t.Fatalf("delivery=%+v err=%v, want the lost exclusion reported without counting the cap as loss", delivery, err)
	}
}
