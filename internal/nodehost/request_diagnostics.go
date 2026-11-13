package nodehost

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// requestDiagnosticsDrain bounds how long shutdown waits for queued timing records.
const requestDiagnosticsDrain = 2 * time.Second

type requestDiagnostic struct {
	write  func(context.Context, nodewire.FrameLog) error
	timing otelutil.OperationTiming
}

type requestDiagnostics struct {
	queue   chan requestDiagnostic
	dropped atomic.Uint64
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closed  bool
	stop    chan struct{}
	done    chan struct{}
}

func newRequestDiagnostics(ctx context.Context) *requestDiagnostics {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	d := &requestDiagnostics{queue: make(chan requestDiagnostic, 64), ctx: ctx, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for {
			select {
			case <-d.stop:
				for {
					select {
					case record := <-d.queue:
						d.deliver(record)
					default:
						return
					}
				}
			case record := <-d.queue:
				d.deliver(record)
			}
		}
	}()
	return d
}

func (d *requestDiagnostics) deliver(record requestDiagnostic) {
	lost := d.dropped.Swap(0)
	raw, err := json.Marshal(struct {
		otelutil.OperationTiming
		Dropped uint64 `json:"dropped"`
	}{record.timing, lost})
	if err != nil || record.write(d.ctx, nodewire.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: "debug", Event: "request_timing", Attrs: raw}) != nil {
		d.dropped.Add(lost + 1)
	}
}

// close stops intake and delivers records already queued. It returns within the
// drain bound; a delivery still running then sees a cancelled context.
func (d *requestDiagnostics) close() {
	d.mu.Lock()
	already := d.closed
	d.closed = true
	d.mu.Unlock()
	if already {
		return
	}
	close(d.stop)
	bound := time.NewTimer(requestDiagnosticsDrain)
	defer bound.Stop()
	select {
	case <-d.done:
	case <-bound.C:
	}
	d.cancel()
}

func (d *requestDiagnostics) observer(write func(context.Context, nodewire.FrameLog) error) func(otelutil.OperationTiming) {
	if write == nil {
		return nil
	}
	return func(timing otelutil.OperationTiming) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closed {
			d.dropped.Add(1)
			return
		}
		select {
		case d.queue <- requestDiagnostic{write: write, timing: timing}:
		default:
			d.dropped.Add(1)
		}
	}
}
