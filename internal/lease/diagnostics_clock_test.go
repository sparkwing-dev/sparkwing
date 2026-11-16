package lease

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type recordedEvents struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordedEvents) sink(_ context.Context, event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recordedEvents) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func collectDiagnostics(t *testing.T) (*Diagnostics, *recordedEvents) {
	t.Helper()
	recorded := &recordedEvents{}
	diagnostics := NewDiagnostics(t.Context(), recorded.sink)
	t.Cleanup(diagnostics.Close)
	return diagnostics, recorded
}

func sealed(t *testing.T, diagnostics *Diagnostics, recorded *recordedEvents) ([]Event, bool) {
	t.Helper()
	// safety: sealing drains every record captured so far, so a missing record fails rather than hangs.
	complete := diagnostics.Seal(t.Context(), recorded.sink)
	return recorded.all(), complete
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
	diagnostics, recorded := collectDiagnostics(t)
	parent, cancel := context.WithTimeout(t.Context(), time.Hour)
	defer cancel()
	deadline, _ := parent.Deadline()
	trace := diagnostics.start(parent, Identity{Role: "OWNER", Run: "r"})
	expires := time.Now().Add(time.Minute)
	trace.emit("watch_arm", time.Time{}, expires, "", 0)
	trace.emit("watch_reset", time.Time{}, expires.Round(0), "", 1)
	all, complete := sealed(t, diagnostics, recorded)
	arm, reset := ofKind(all, "watch_arm"), ofKind(all, "watch_reset")
	if !complete || len(arm) != 1 || len(reset) != 1 {
		t.Fatalf("complete=%v records=%+v", complete, all)
	}
	if arm[0].ExpiresMono != expires.Sub(diagnostics.origin).Nanoseconds() || arm[0].ExpiresMono <= 0 {
		t.Fatalf("expiry offset=%d, want %d", arm[0].ExpiresMono, expires.Sub(diagnostics.origin).Nanoseconds())
	}
	if arm[0].DeadlineMono != deadline.Sub(diagnostics.origin).Nanoseconds() || arm[0].DeadlineMono <= 0 {
		t.Fatalf("deadline offset=%d, want %d", arm[0].DeadlineMono, deadline.Sub(diagnostics.origin).Nanoseconds())
	}
	// safety: a wall-only target has no monotonic equivalent, so its offset stays absent.
	if reset[0].ExpiresMono != 0 {
		t.Fatalf("wall-only expiry reported a monotonic offset %d", reset[0].ExpiresMono)
	}
}

func TestKeeperConvertsServerExpiryToMonotonicOnArrival(t *testing.T) {
	// safety: synctest bubble time carries no monotonic reading, so this runs on the real clock. A
	// startup capture can lose the selection record to contention; only a window that reports
	// that loss is retried, a bounded number of times.
	for attempt := 1; ; attempt++ {
		diagnostics, recorded := collectDiagnostics(t)
		server := time.Now().Add(30 * time.Second).Round(0)
		renewed := make(chan struct{})
		keeper := Keeper{
			Lease: time.Minute, Interval: time.Hour, Diagnostics: diagnostics, Identity: Identity{Role: "OWNER", Run: "r"},
			Renew:   func(context.Context) (Beat, error) { return Beat{ExpiresAt: server}, nil },
			Observe: func(Beat) { close(renewed) },
		}
		_, stop := keeper.Run(t.Context())
		<-renewed
		all, complete := sealed(t, diagnostics, recorded)
		stop()
		selected := ofKind(all, "expiry_selected")
		if len(selected) == 0 && !complete && attempt < 5 {
			continue
		}
		wall := server.Sub(diagnostics.origin.Round(0)).Nanoseconds()
		if len(selected) != 1 || !selected[0].ExpiresAt.Equal(server) || selected[0].ExpiresMono == 0 ||
			max(selected[0].ExpiresMono-wall, wall-selected[0].ExpiresMono) > int64(time.Second) {
			t.Fatalf("complete=%v selections %+v, want the server expiry on the monotonic clock near %d", complete, selected, wall)
		}
		return
	}
}

func TestKeeperStopsAtTheServerWallExpiryAfterSuspend(t *testing.T) {
	// safety: the injected wall clock has passed the server expiry while the monotonic expiry has
	// not, as on a host that resumes from suspend with its network still down.
	server := time.Now().Add(30 * time.Second).Round(0)
	renewed, calls := make(chan struct{}), 0
	keeper := Keeper{
		Lease: time.Minute, Interval: time.Hour, wall: func() time.Time { return server.Add(time.Second) },
		Renew:   func(context.Context) (Beat, error) { calls++; return Beat{ExpiresAt: server}, nil },
		Observe: func(Beat) { close(renewed) },
	}
	unit, stop := keeper.Run(t.Context())
	defer stop()
	select {
	case <-renewed:
		t.Fatal("unit kept running past the server's wall expiry")
	case <-unit.Done():
	}
	if !errors.Is(context.Cause(unit), ErrSilent) || calls != 1 {
		t.Fatalf("cause=%v renewals=%d", context.Cause(unit), calls)
	}
}

func TestKeeperDropsTheServerWallBoundWhenARenewalOmitsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var skew atomic.Int64
		renewals := make(chan int)
		calls := 0
		keeper := Keeper{
			Lease: time.Minute, Interval: 10 * time.Second, wall: func() time.Time { return time.Now().Add(time.Duration(skew.Load())) },
			Renew: func(context.Context) (Beat, error) {
				calls++
				if calls == 1 {
					return Beat{ExpiresAt: start.Add(30 * time.Second).Round(0)}, nil
				}
				return Beat{}, nil
			},
			Observe: func(Beat) { renewals <- calls },
		}
		unit, stop := keeper.Run(t.Context())
		defer stop()
		<-renewals
		<-renewals
		// safety: the second renewal carried no server expiry, so a wall clock past the first one no longer binds.
		skew.Store(int64(time.Minute))
		if got := <-renewals; got != 3 || unit.Err() != nil {
			t.Fatalf("renewal=%d err=%v cause=%v", got, unit.Err(), context.Cause(unit))
		}
	})
}

func TestKeeperRecordsRenewalErrorCodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics, recorded := collectDiagnostics(t)
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
		all := recorded.all()
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
				diagnostics, recorded := collectDiagnostics(t)
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
				all := recorded.all()
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

func TestKeeperMarksExpiriesTheServerWallBounds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		diagnostics, recorded := collectDiagnostics(t)
		start := time.Now()
		beats := []Beat{{ExpiresAt: start.Add(30 * time.Second).Round(0)}, {}, {ExpiresAt: start.Add(time.Hour).Round(0)}}
		calls := 0
		renewed := make(chan int)
		keeper := Keeper{
			Lease: time.Minute, Interval: 10 * time.Second, Diagnostics: diagnostics, Identity: Identity{Role: "OWNER", Run: "r"},
			Renew: func(context.Context) (Beat, error) {
				quietStart(&calls)
				return beats[calls-1], nil
			},
			Observe: func(Beat) { renewed <- calls },
		}
		_, stop := keeper.Run(t.Context())
		for range beats {
			<-renewed
		}
		synctest.Wait()
		all := recorded.all()
		stop()
		var marks []bool
		for _, event := range ofKind(all, "expiry_selected") {
			marks = append(marks, event.ExpiresWallBound)
		}
		// safety: only the first selection is the server's expiry; an absent or later one leaves the local bound.
		if len(marks) != 3 || !marks[0] || marks[1] || marks[2] {
			t.Fatalf("expiry_selected wall-bound marks=%v in %+v", marks, all)
		}
		if resets := ofKind(all, "watch_reset"); len(resets) == 0 || !resets[0].ExpiresWallBound {
			t.Fatalf("first watch_reset lacks the wall-bound mark: %+v", resets)
		}
	})
}

func TestKeeperStopsWithoutRenewingAfterResumeWithNetworkDown(t *testing.T) {
	// safety: after a resume the wall clock is past the server expiry while renewals fail
	// transiently, so the loop must stop at its top or post-renewal check instead of retrying
	// until the monotonic expiry.
	for _, phase := range []string{"before renewal", "during renewal"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var skew atomic.Int64
				calls := 0
				renewed := make(chan struct{}, 1)
				keeper := Keeper{
					Lease: time.Minute, Interval: 10 * time.Second,
					wall: func() time.Time { return time.Now().Add(time.Duration(skew.Load())) },
					Renew: func(context.Context) (Beat, error) {
						calls++
						if calls == 1 {
							return Beat{ExpiresAt: start.Add(30 * time.Second).Round(0)}, nil
						}
						if phase == "during renewal" {
							skew.Store(int64(time.Minute))
						}
						return Beat{}, &apierr.Error{Code: hosterr.Unavailable}
					},
					Observe: func(Beat) { renewed <- struct{}{} },
				}
				unit, stop := keeper.Run(t.Context())
				defer stop()
				<-renewed
				want := 2
				if phase == "before renewal" {
					skew.Store(int64(time.Minute))
					want = 1
				}
				<-unit.Done()
				// safety: both phases must stop at the 10 s wake itself, not one renewal interval later.
				if !errors.Is(context.Cause(unit), ErrSilent) || calls != want || time.Since(start) != 10*time.Second {
					t.Fatalf("cause=%v renewals=%d after %v, want ErrSilent after %d at 10s", context.Cause(unit), calls, time.Since(start), want)
				}
			})
		})
	}
}
