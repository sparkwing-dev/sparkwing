package lease

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type captureDoneContext struct {
	context.Context
	calls atomic.Int64
}

func (c *captureDoneContext) Done() <-chan struct{} {
	c.calls.Add(1)
	return c.Context.Done()
}

func newCaptureTestTrace(t *testing.T) *keeperTrace {
	t.Helper()
	base, cancel := context.WithCancel(t.Context())
	ctx := &captureDoneContext{Context: base}
	d := &Diagnostics{ctx: ctx, done: ctx.Done(), cancel: cancel, origin: processDiagnosticOrigin, queue: make(chan diagnosticRecord, 1)}
	t.Cleanup(d.Close)
	return d.start(t.Context(), Identity{})
}

func TestKeeperCaptureDoesNotCallContextAfterConstruction(t *testing.T) {
	trace := newCaptureTestTrace(t)
	trace.emit("execution_end", time.Time{}, time.Time{}, "ended", 0)
	if calls := trace.diagnostics.ctx.(*captureDoneContext).calls.Load(); calls != 1 {
		t.Fatalf("Done calls=%d, want only the construction call", calls)
	}
}

func TestKeeperFirstCaptureDoesNotAllocateUnderLock(t *testing.T) {
	traces := make([]*keeperTrace, 102)
	for i := range traces {
		traces[i] = newCaptureTestTrace(t)
	}
	i := 0
	allocations := testing.AllocsPerRun(100, func() { traces[i].emit("watch_arm", time.Time{}, time.Time{}, "", 0); i++ })
	if allocations != 0 {
		t.Fatalf("first capture allocates %.0f objects", allocations)
	}
	for _, trace := range traces {
		if calls := trace.diagnostics.ctx.(*captureDoneContext).calls.Load(); calls != 1 {
			t.Fatalf("capture called Done: %d calls", calls)
		}
	}
}

func TestKeeperInitializedCaptureDoesNotAllocateUnderLock(t *testing.T) {
	trace := newCaptureTestTrace(t)
	allocations := testing.AllocsPerRun(100, func() { trace.emit("watch_arm", time.Time{}, time.Time{}, "", 0) })
	if allocations != 0 {
		t.Fatalf("initialized capture allocates %.0f objects", allocations)
	}
	if calls := trace.diagnostics.ctx.(*captureDoneContext).calls.Load(); calls != 1 {
		t.Fatalf("capture called Done: %d calls", calls)
	}
}

func TestKeeperClosedRecorderCountsCaptureLoss(t *testing.T) {
	d := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
	defer d.Close()
	if d.done == nil {
		t.Fatal("constructor did not cache cancellation")
	}
	select {
	case <-d.done:
		t.Fatal("fresh recorder is closed")
	default:
	}
	trace := d.start(t.Context(), Identity{})
	d.Close()
	select {
	case <-d.done:
	default:
		t.Fatal("cached cancellation does not follow Close")
	}
	before := trace.lost.Load()
	trace.emit("execution_end", time.Time{}, time.Time{}, "ended", 0)
	if trace.lost.Load() != before+1 {
		t.Fatal("closed recorder accepted useful capture")
	}
}
