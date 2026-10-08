package nodemetrics

import (
	"context"
	"time"
)

type ReadingForTest struct {
	At      time.Time
	CPU     time.Duration
	Memory  int64
	Invalid bool
}

func AttachReadingsForTest(ctx context.Context, sink Sink, readings ...ReadingForTest) func() (Delivery, error) {
	index := 0
	return attach(ctx, sink, func() reading {
		value := readings[min(index, len(readings)-1)]
		index++
		return reading{at: value.At, cpu: value.CPU, memory: value.Memory, valid: !value.Invalid}
	})
}
