package lease

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func collectDiagnostics(t *testing.T) (*Diagnostics, <-chan Event) {
	t.Helper()
	events := make(chan Event, 256)
	diagnostics := NewDiagnostics(t.Context(), func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	t.Cleanup(diagnostics.Close)
	return diagnostics, events
}

func nextKind(t *testing.T, events <-chan Event, kind string) Event {
	t.Helper()
	for event := range events {
		if event.Kind == kind {
			return event
		}
	}
	t.Fatalf("no %s event", kind)
	return Event{}
}

func delivered(events <-chan Event) []Event {
	var all []Event
	for {
		select {
		case event := <-events:
			all = append(all, event)
		default:
			return all
		}
	}
}

func ofKind(events []Event, kind string) []Event {
	var matched []Event
	for _, event := range events {
		if event.Kind == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

func quietStart(calls *int) {
	// safety: concurrent startup captures can drop one another, so the first renewal waits for the
	// watcher to arm; these tests assert exact records rather than loss accounting.
	*calls++
	if *calls == 1 {
		synctest.Wait()
	}
}

func TestKeeperDiagnosticsRecordMonotonicTargets(t *testing.T) {
	diagnostics, events := collectDiagnostics(t)
	parent, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	deadline, _ := parent.Deadline()
	trace := diagnostics.start(parent, Identity{Role: "OWNER", Run: "r"})
	expires := time.Now().Add(time.Minute)
	trace.emit("watch_arm", time.Time{}, expires, "", 0)
	trace.emit("watch_reset", time.Time{}, expires.Round(0), "", 1)
	arm := nextKind(t, events, "watch_arm")
	if arm.ExpiresMono != expires.Sub(diagnostics.origin).Nanoseconds() || arm.ExpiresMono <= 0 {
		t.Fatalf("expiry offset=%d, want %d", arm.ExpiresMono, expires.Sub(diagnostics.origin).Nanoseconds())
	}
	if arm.DeadlineMono != deadline.Sub(diagnostics.origin).Nanoseconds() || arm.DeadlineMono <= 0 {
		t.Fatalf("deadline offset=%d, want %d", arm.DeadlineMono, deadline.Sub(diagnostics.origin).Nanoseconds())
	}
	// safety: a wall-only target has no monotonic equivalent, so its offset stays absent.
	if reset := nextKind(t, events, "watch_reset"); reset.ExpiresMono != 0 {
		t.Fatalf("wall-only expiry reported a monotonic offset %d", reset.ExpiresMono)
	}
}

func TestKeeperConvertsServerExpiryToMonotonicOnArrival(t *testing.T) {
	// safety: synctest bubble time carries no monotonic reading, so this runs on the real clock.
	diagnostics, events := collectDiagnostics(t)
	server := time.Now().Add(30 * time.Second).Round(0)
	keeper := Keeper{
		Lease: time.Minute, Interval: time.Hour, Diagnostics: diagnostics, Identity: Identity{Role: "OWNER", Run: "r"},
		Renew: func(context.Context) (Beat, error) { return Beat{ExpiresAt: server}, nil },
	}
	_, stop := keeper.Run(t.Context())
	defer stop()
	// safety: the watcher records its reset after the handoff, when no other capture runs.
	reset := nextKind(t, events, "watch_reset")
	wall := server.Sub(diagnostics.origin.Round(0)).Nanoseconds()
	if !reset.ExpiresAt.Equal(server) || reset.ExpiresMono == 0 || max(reset.ExpiresMono-wall, wall-reset.ExpiresMono) > int64(time.Second) {
		t.Fatalf("watch_reset expiry=%s offset=%d, want the server expiry on the monotonic clock near %d", reset.ExpiresAt, reset.ExpiresMono, wall)
	}
}

func TestKeeperRecordsRenewalErrorCodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics, events := collectDiagnostics(t)
		failures := []error{&apierr.Error{Code: hosterr.Unavailable}, errors.New("transport reset"), &apierr.Error{Code: hosterr.LockHeld}}
		calls := 0
		keeper := Keeper{
			Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Identity: Identity{Role: "NODE", Run: "r"},
			Renew: func(context.Context) (Beat, error) {
				quietStart(&calls)
				return Beat{}, failures[calls-1]
			},
		}
		unit, stop := keeper.Run(t.Context())
		<-unit.Done()
		stop()
		synctest.Wait()
		all := delivered(events)
		finishes := ofKind(all, "renewal_finish")
		if len(finishes) != 3 {
			t.Fatalf("renewal_finish records=%d in %+v", len(finishes), all)
		}
		for i, want := range []string{"unavailable", "unclassified", "lock_held"} {
			if finishes[i].Outcome != "error" || finishes[i].ErrorCode != want {
				t.Fatalf("renewal_finish outcome=%q code=%q, want error %q", finishes[i].Outcome, finishes[i].ErrorCode, want)
			}
		}
		if !errors.Is(context.Cause(unit), ErrLeaseLost) {
			t.Fatalf("cause=%v", context.Cause(unit))
		}
	})
}

func TestKeeperRecordsCancellationRequestsBeforeActing(t *testing.T) {
	for _, source := range []string{"renewal", "wait"} {
		t.Run(source, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				diagnostics, events := collectDiagnostics(t)
				requested, renewed := make(chan struct{}), make(chan struct{})
				calls := 0
				keeper := Keeper{
					Lease: time.Minute, Interval: time.Second, Diagnostics: diagnostics, Identity: Identity{Role: "NODE", Run: "r"},
					Renew: func(context.Context) (Beat, error) {
						quietStart(&calls)
						return Beat{CancelRequested: source == "renewal", CancelReason: "user"}, nil
					},
					Observe: func(Beat) {
						if calls == 1 {
							close(renewed)
						}
					},
					WaitCancellation: func(ctx context.Context) *CancelError {
						select {
						case <-ctx.Done():
							return nil
						case <-requested:
							return &CancelError{Reason: "user"}
						}
					},
				}
				unit, stop := keeper.Run(t.Context())
				<-renewed
				if source == "wait" {
					synctest.Wait()
					close(requested)
				}
				<-unit.Done()
				diagnostics.lifecycle.Lock()
				trace := diagnostics.traces[0]
				diagnostics.lifecycle.Unlock()
				trace.mu.Lock()
				capturedAtCancel := trace.sequence
				trace.mu.Unlock()
				stop()
				synctest.Wait()
				all := delivered(events)
				requests := ofKind(all, "cancel_requested")
				if len(requests) != 1 {
					t.Fatalf("cancel records=%d in %+v", len(requests), all)
				}
				if event := requests[0]; event.CancelSource != source || event.Sequence > capturedAtCancel {
					t.Fatalf("cancel record source=%q sequence=%d, captured at cancel=%d", event.CancelSource, event.Sequence, capturedAtCancel)
				}
			})
		})
	}
}
