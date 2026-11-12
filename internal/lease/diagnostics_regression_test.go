package lease

import (
	"context"
	"testing"
	"time"
)

func TestKeeperDiagnosticContentionCannotDelayWatcher(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "absent"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			var trace *keeperTrace
			var diagnostics *Diagnostics
			armed := make(chan struct{}, 1)
			if enabled {
				diagnostics = NewDiagnostics(t.Context(), func(_ context.Context, e Event) error {
					if e.Kind == "watch_arm" {
						armed <- struct{}{}
					}
					return nil
				})
				defer diagnostics.Close()
				trace = diagnostics.start(t.Context(), Identity{})
			}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			updates := make(chan leaseUpdate)
			expired := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				watch(ctx, func(error) { close(expired); stop() }, updates, time.Now().Add(time.Hour), trace)
			}()
			if enabled {
				<-armed
				trace.mu.Lock()
			}
			// safety: Holding the capture mutex models a renewal goroutine descheduled in emit.
			updates <- leaseUpdate{expires: time.Now().Add(10 * time.Millisecond), renewal: 1}
			timely := false
			select {
			case <-expired:
				timely = true
			case <-time.After(100 * time.Millisecond):
			}
			if enabled {
				trace.mu.Unlock()
			}
			<-expired
			<-done
			if !timely {
				t.Fatal("watcher installed a 10ms expiry but could not cancel within 100ms while diagnostics mutex was held")
			}
		})
	}
}

func TestKeeperDiagnosticsShareProcessMonotonicOrigin(t *testing.T) {
	first := NewDiagnostics(t.Context(), nil)
	defer first.Close()
	second := NewDiagnostics(t.Context(), nil)
	defer second.Close()
	if !first.origin.Equal(second.origin) {
		t.Fatalf("same process uses two origins: %s and %s", first.origin, second.origin)
	}
}

func TestKeeperDiagnosticContentionCountsLossWithoutBlockingEnd(t *testing.T) {
	events := make(chan Event, 1)
	d := NewDiagnostics(t.Context(), func(_ context.Context, e Event) error { events <- e; return nil })
	defer d.Close()
	trace := d.start(t.Context(), Identity{})
	trace.mu.Lock()
	done := make(chan struct{})
	go func() { trace.emit("execution_end", time.Time{}, time.Time{}, "cancelled", 0); close(done) }()
	timely := false
	select {
	case <-done:
		timely = true
	case <-time.After(time.Second):
	}
	trace.mu.Unlock()
	<-done
	if !timely {
		t.Fatal("execution-end capture waited for diagnostic lock")
	}
	trace.emit("watch_reset", time.Time{}, time.Now(), "", 1)
	e := <-events
	if e.Dropped != 1 {
		t.Fatalf("lost capture was not reported: %+v", e)
	}
}

func TestKeeperInstancesAreUniqueAcrossRecorders(t *testing.T) {
	a := NewDiagnostics(t.Context(), nil)
	defer a.Close()
	b := NewDiagnostics(t.Context(), nil)
	defer b.Close()
	if a.start(t.Context(), Identity{}).instance == b.start(t.Context(), Identity{}).instance {
		t.Fatal("recorders reused an instance identity")
	}
}
