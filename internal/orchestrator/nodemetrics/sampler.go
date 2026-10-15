package nodemetrics

import (
	"sync/atomic"
	"time"
)

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
