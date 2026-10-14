package nodemetrics

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type Sample struct {
	Valid         bool
	TS            time.Time
	CPUMillicores int64
	MemoryBytes   int64
}

type Sink interface {
	Push(context.Context, Sample) error
}

// ErrSinkFull is what a Sink returns to stop receiving samples for good.
var ErrSinkFull = errors.New("nodemetrics: sink holds no more samples")

const defaultInterval = 2 * time.Second

var intervalNanos atomic.Int64

func SetIntervalForTest(d time.Duration) (restore func()) {
	previous := intervalNanos.Swap(int64(d))
	return func() { intervalNanos.Store(previous) }
}

func Interval() time.Duration {
	if d := time.Duration(intervalNanos.Load()); d > 0 {
		return d
	}
	return defaultInterval
}

// Delivery reports attempted and lost samples, excluding a full sink.
// LostInvalid counts lost samples that marked the measurement invalid; losing
// one loses an exclusion, not just a reading.
type Delivery struct {
	Attempted   int64
	Lost        int64
	LostInvalid int64
}
