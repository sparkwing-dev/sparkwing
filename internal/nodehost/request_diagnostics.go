package nodehost

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

type requestDiagnostic struct {
	write  func(context.Context, nodewire.FrameLog) error
	timing otelutil.OperationTiming
}
type requestDiagnostics struct {
	queue   chan requestDiagnostic
	dropped atomic.Uint64
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
}

func newRequestDiagnostics(ctx context.Context) *requestDiagnostics {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d := &requestDiagnostics{queue: make(chan requestDiagnostic, 64), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for {
			select {
			case <-ctx.Done():
				return
			case record := <-d.queue:
				lost := d.dropped.Swap(0)
				raw, err := json.Marshal(struct {
					otelutil.OperationTiming
					Dropped uint64 `json:"dropped"`
				}{record.timing, lost})
				if err != nil || record.write(ctx, nodewire.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: "debug", Event: "request_timing", Attrs: raw}) != nil {
					d.dropped.Add(lost + 1)
				}
			}
		}
	}()
	return d
}

func (d *requestDiagnostics) observer(write func(context.Context, nodewire.FrameLog) error) func(otelutil.OperationTiming) {
	if write == nil {
		return nil
	}
	return func(timing otelutil.OperationTiming) {
		select {
		case <-d.ctx.Done():
			return
		default:
		}
		select {
		case d.queue <- requestDiagnostic{write: write, timing: timing}:
		default:
			d.dropped.Add(1)
		}
	}
}
