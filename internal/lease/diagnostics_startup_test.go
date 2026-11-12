package lease

import (
	"context"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// safety: Model the state before the delivery worker first evaluates ctx.Done().
func newUnstartedDiagnosticTrace(t *testing.T) *keeperTrace {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d := &Diagnostics{ctx: ctx, origin: processDiagnosticOrigin, queue: make(chan diagnosticRecord, 1)}
	return d.start(t.Context(), Identity{})
}

func TestKeeperFirstCaptureDoesNotAllocateUnderLock(t *testing.T) {
	traces := make([]*keeperTrace, 102)
	for i := range traces {
		traces[i] = newUnstartedDiagnosticTrace(t)
	}
	i := 0
	allocations := testing.AllocsPerRun(100, func() { traces[i].emit("watch_arm", time.Time{}, time.Time{}, "", 0); i++ })
	if allocations != 0 {
		t.Fatalf("first capture allocates %.0f objects while owning trace.mu (lazy ctx.Done channel)", allocations)
	}
}

func TestKeeperFirstCaptureDoesNotWaitForDeliveryContextMutex(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	d := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
	defer d.Close()
	trace := d.start(t.Context(), Identity{})
	// safety: Go 1.26 cancelCtx.Done initializes its channel under cancelCtx.mu.
	// Model the delivery worker descheduled while owning that initialization lock.
	value := reflect.ValueOf(d.ctx).Elem()
	mu := (*sync.Mutex)(unsafe.Pointer(value.FieldByName("mu").UnsafeAddr()))
	doneState := (*atomic.Value)(unsafe.Pointer(value.FieldByName("done").UnsafeAddr()))
	mu.Lock()
	if doneState.Load() == nil {
		mu.Unlock()
		t.Fatal("constructor returned before initializing the delivery Done channel")
	}
	t.Log("constructor initialized Done before capture")
	done := make(chan struct{})
	go func() { trace.emit("execution_end", time.Time{}, time.Time{}, "ended", 0); close(done) }()
	timely := false
	select {
	case <-done:
		timely = true
	case <-time.After(100 * time.Millisecond):
	}
	mu.Unlock()
	<-done
	if !timely {
		t.Fatal("capture waited for delivery context mutex despite TryLock on trace.mu")
	}
}

func TestKeeperInitializedCaptureDoesNotAllocateUnderLock(t *testing.T) {
	trace := newUnstartedDiagnosticTrace(t)
	_ = trace.diagnostics.ctx.Done()
	allocations := testing.AllocsPerRun(100, func() { trace.emit("watch_arm", time.Time{}, time.Time{}, "", 0) })
	if allocations != 0 {
		t.Fatalf("initialized capture still allocates %.0f objects", allocations)
	}
}

func TestKeeperClosedRecorderCountsCaptureLoss(t *testing.T) {
	d := NewDiagnostics(t.Context(), func(context.Context, Event) error { return nil })
	trace := d.start(t.Context(), Identity{})
	d.Close()
	before := trace.lost.Load()
	trace.emit("execution_end", time.Time{}, time.Time{}, "ended", 0)
	if trace.lost.Load() != before+1 {
		t.Fatal("closed recorder accepted useful capture")
	}
}
