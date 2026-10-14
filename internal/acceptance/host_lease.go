package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
)

// CheckKeeperCauses exercises renewal causes and liveness at the host-library boundary.
func CheckKeeperCauses(t *testing.T) {
	t.Helper()
	t.Run("rejected_owner_and_node", checkRejectedOwnerAndNode)
	t.Run("cancellation_and_watch", checkCancellationAndWatch)
	t.Run("silence", checkSilence)
	t.Run("retry_after", checkRetryAfter)
	t.Run("success_resets_silence", checkSuccessfulRenewal)
	t.Run("parent_cause", checkParentCancellation)
}

// CheckKeeperConflictAndStop exercises canonical conflict handling and joined shutdown.
func CheckKeeperConflictAndStop(t *testing.T) {
	t.Helper()
	t.Run("authority_rejection", checkRejectedOwnerAndNode)
	t.Run("illegal_transition", checkIllegalTransition)
	t.Run("stop_joins", checkStopJoins)
}

func checkRejectedOwnerAndNode(t *testing.T) {
	for _, code := range []apierr.Code{apierr.LockHeld, apierr.Unauthenticated, apierr.Forbidden} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				keeper := lease.Keeper{
					Interval: 10 * time.Second,
					Lease:    time.Minute,
					Renew: func(context.Context) (lease.Beat, error) {
						calls++
						return lease.Beat{}, fmt.Errorf("renew: %w", &apierr.Error{Code: code})
					},
				}
				unit, stop := keeper.Run(context.Background())
				defer stop()
				synctest.Wait()
				if !errors.Is(context.Cause(unit), lease.ErrLeaseLost) {
					t.Fatalf("cause = %v, want lease loss", context.Cause(unit))
				}
				if calls != 1 {
					t.Fatalf("renewals = %d, want immediate first renewal only", calls)
				}
			})
		})
	}
}

func checkCancellationAndWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		observed := make(chan lease.Beat, 1)
		keeper := lease.Keeper{
			Interval: 10 * time.Second,
			Lease:    time.Minute,
			Renew: func(context.Context) (lease.Beat, error) {
				return lease.Beat{CancelRequested: true, Watched: true, WatchedNodes: []string{"job"}}, nil
			},
			Observe: func(beat lease.Beat) { observed <- beat },
		}
		unit, stop := keeper.Run(parent)
		defer stop()
		synctest.Wait()
		var cause *lease.CancelError
		if !errors.As(context.Cause(unit), &cause) {
			t.Fatalf("cause = %v, want requested cancellation", context.Cause(unit))
		}
		if cause.Error() == "" {
			t.Fatal("cancellation has no diagnostic")
		}
		select {
		case beat := <-observed:
			if !beat.Watched || len(beat.WatchedNodes) != 1 || beat.WatchedNodes[0] != "job" {
				t.Fatalf("observed = %+v", beat)
			}
		default:
			t.Fatal("successful cancellation beat did not deliver its watch update")
		}
		cancel(errors.New("later parent cancellation"))
		stop()
		if context.Cause(unit) != cause {
			t.Fatalf("first cause changed to %v", context.Cause(unit))
		}
	})
}

func checkSilence(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%t", blocked), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				keeper := lease.Keeper{
					Interval: 10 * time.Second,
					Lease:    time.Minute,
					Renew: func(ctx context.Context) (lease.Beat, error) {
						calls++
						if blocked {
							<-ctx.Done()
						}
						return lease.Beat{}, errors.New("controller unreachable")
					},
					Observe: func(lease.Beat) { t.Error("failed renewal reached Observe") },
				}
				unit, stop := keeper.Run(context.Background())
				defer stop()
				synctest.Wait()
				if unit.Err() != nil {
					t.Fatalf("transient failure ended execution: %v", context.Cause(unit))
				}
				<-time.After(time.Minute)
				synctest.Wait()
				if !errors.Is(context.Cause(unit), lease.ErrSilent) {
					t.Fatalf("cause = %v, want silent at one full lease", context.Cause(unit))
				}
				stop()
				stoppedCalls := calls
				<-time.After(time.Minute)
				synctest.Wait()
				if calls != stoppedCalls {
					t.Fatal("renewals outlived stop")
				}
			})
		})
	}
}

func checkRetryAfter(t *testing.T) {
	for _, code := range []apierr.Code{apierr.RateLimited, apierr.Unavailable, apierr.QuotaExceeded} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var failure apierr.Error
				if err := json.Unmarshal([]byte(fmt.Sprintf(`{"code":%q,"message":"try later","details":{"retry_after_ms":25000}}`, code)), &failure); err != nil {
					t.Fatal(err)
				}
				var calls []time.Duration
				start := time.Now()
				keeper := lease.Keeper{
					Interval: 10 * time.Second,
					Lease:    time.Minute,
					Renew: func(context.Context) (lease.Beat, error) {
						calls = append(calls, time.Since(start))
						return lease.Beat{}, fmt.Errorf("renew: %w", &failure)
					},
				}
				unit, stop := keeper.Run(context.Background())
				defer stop()
				<-time.After(time.Minute)
				synctest.Wait()
				if !errors.Is(context.Cause(unit), lease.ErrSilent) {
					t.Fatalf("cause = %v, want silent", context.Cause(unit))
				}
				if len(calls) != 3 || calls[0] != 0 || calls[1] != 25*time.Second || calls[2] != 50*time.Second {
					t.Fatalf("renewals at %v, want [0s 25s 50s]", calls)
				}
			})
		})
	}
}

func checkSuccessfulRenewal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		var calls []time.Duration
		var observed int
		keeper := lease.Keeper{
			Interval: 10 * time.Second,
			Lease:    time.Minute,
			Renew: func(context.Context) (lease.Beat, error) {
				calls = append(calls, time.Since(start))
				if len(calls) <= 3 {
					return lease.Beat{Watched: true, WatchedNodes: []string{"job"}}, nil
				}
				return lease.Beat{}, errors.New("offline")
			},
			Observe: func(lease.Beat) {
				observed++
				if observed == 1 {
					<-time.After(3 * time.Second)
				}
			},
		}
		unit, stop := keeper.Run(context.Background())
		defer stop()
		<-time.After(79 * time.Second)
		synctest.Wait()
		if unit.Err() != nil || len(calls) != 8 || observed != 3 {
			t.Fatalf("before expiry: cause=%v calls=%v observed=%d", context.Cause(unit), calls, observed)
		}
		<-time.After(time.Second)
		synctest.Wait()
		if !errors.Is(context.Cause(unit), lease.ErrSilent) || len(calls) != 8 {
			t.Fatalf("at expiry: cause=%v calls=%v", context.Cause(unit), calls)
		}
	})
}

func checkStopJoins(t *testing.T) {
	for _, blockObserver := range []bool{false, true} {
		t.Run(fmt.Sprintf("observer=%t", blockObserver), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				blocked := make(chan struct{})
				release := make(chan struct{})
				keeper := lease.Keeper{
					Interval: 10 * time.Second,
					Lease:    time.Minute,
					Renew: func(context.Context) (lease.Beat, error) {
						if !blockObserver {
							close(blocked)
							<-release
						}
						return lease.Beat{}, nil
					},
					Observe: func(lease.Beat) {
						if !blockObserver {
							t.Error("late renewal invoked Observe after stop")
							return
						}
						close(blocked)
						<-release
					},
				}
				unit, stop := keeper.Run(context.Background())
				<-blocked
				stopped := make(chan struct{})
				go func() {
					stop()
					close(stopped)
				}()
				synctest.Wait()
				select {
				case <-stopped:
					t.Error("stop returned with work still in flight")
				default:
				}
				if !errors.Is(context.Cause(unit), context.Canceled) {
					t.Errorf("stop cause = %v", context.Cause(unit))
				}
				close(release)
				<-stopped
				stop()
			})
		})
	}
}

func checkParentCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		started := make(chan struct{})
		keeper := lease.Keeper{
			Interval: 10 * time.Second,
			Lease:    time.Minute,
			Renew: func(ctx context.Context) (lease.Beat, error) {
				close(started)
				<-ctx.Done()
				return lease.Beat{CancelRequested: true}, &apierr.Error{Code: apierr.LockHeld}
			},
		}
		unit, stop := keeper.Run(parent)
		defer stop()
		<-started
		cause := errors.New("owner stopped")
		cancel(cause)
		stop()
		if context.Cause(unit) != cause {
			t.Fatalf("cause = %v, want parent cause", context.Cause(unit))
		}
	})
}

func checkIllegalTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		keeper := lease.Keeper{
			Interval: 10 * time.Second,
			Lease:    time.Minute,
			Renew: func(context.Context) (lease.Beat, error) {
				calls++
				if calls == 1 {
					return lease.Beat{}, &apierr.Error{Code: apierr.IllegalTransition}
				}
				return lease.Beat{}, nil
			},
		}
		unit, stop := keeper.Run(context.Background())
		defer stop()
		<-time.After(10 * time.Second)
		synctest.Wait()
		if unit.Err() != nil || calls != 2 {
			t.Fatalf("after generic 409: cause=%v renewals=%d, want live after successful retry", context.Cause(unit), calls)
		}
	})
}
