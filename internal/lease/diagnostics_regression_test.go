package lease

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestKeeperDiagnosticContentionCannotDelayWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		trace := newCaptureTestTrace(t)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		updates := make(chan leaseUpdate)
		expired, joined := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(joined)
			watch(ctx, func(err error) { cancel(err); close(expired) }, updates, time.Now().Add(time.Hour), trace)
		}()
		arm := <-trace.diagnostics.queue
		if arm.event.Kind != "watch_arm" {
			t.Fatalf("unexpected first capture: %+v", arm.event)
		}
		trace.mu.Lock()
		defer trace.mu.Unlock()
		updates <- leaseUpdate{expires: time.Now().Add(time.Second), renewal: 1}
		synctest.Wait()
		if trace.lost.Load() != 1 {
			t.Fatal("watch reset did not report capture contention")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-expired:
		default:
			t.Fatal("shortened expiry did not cancel while capture was held")
		}
		if !errors.Is(context.Cause(ctx), ErrSilent) {
			t.Fatalf("expiry cause=%v", context.Cause(ctx))
		}
		<-joined
	})
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
	for _, kind := range []string{"watch_reset", "execution_end"} {
		t.Run(kind, func(t *testing.T) {
			trace := newCaptureTestTrace(t)
			held, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() { trace.mu.Lock(); close(held); <-release; trace.mu.Unlock(); close(joined) }()
			<-held
			defer func() {
				select {
				case <-joined:
					return
				default:
					close(release)
					<-joined
				}
			}()
			trace.emit(kind, time.Time{}, time.Time{}, "ended", 0)
			if trace.lost.Load() != 1 {
				t.Fatal("held capture was not counted as loss")
			}
			close(release)
			<-joined
			trace.emit("watch_reset", time.Time{}, time.Now(), "", 1)
			event := <-trace.diagnostics.queue
			if event.event.Dropped != 1 {
				t.Fatalf("lost capture was not reported: %+v", event.event)
			}
		})
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
