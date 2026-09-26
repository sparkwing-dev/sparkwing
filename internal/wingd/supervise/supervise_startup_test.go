package supervise

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestASlowStartingDaemonIsNotReplaced(t *testing.T) {
	child := newSupervisorTestChild()
	var starts, probes atomic.Int32
	answering := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Loop(ctx, Config{
			ProbeInterval:     time.Millisecond,
			ProbeTimeout:      time.Millisecond,
			FailureLimit:      2,
			TermGrace:         time.Millisecond,
			StartupTimeout:    time.Minute,
			RestartBackoff:    time.Millisecond,
			MaxRestartBackoff: time.Millisecond,
		}, Deps{
			Start: func() (Child, error) {
				starts.Add(1)
				return child, nil
			},
			Probe: func(context.Context) error {
				switch n := probes.Add(1); {
				case n <= 20:
					return errors.New("still opening the store")
				case n == 21:
					close(answering)
				}
				return nil
			},
		})
	}()

	select {
	case <-answering:
	case <-time.After(5 * time.Second):
		t.Fatal("the starting daemon was never probed into readiness")
	}
	if terms, kills := child.actions(); terms != 0 || kills != 0 || starts.Load() != 1 {
		t.Fatalf("a daemon failing 20 probes before its first answer was stopped (terms %d, kills %d, starts %d); "+
			"it is starting, not unresponsive", terms, kills, starts.Load())
	}
	cancel()
	child.done <- nil
	if err := <-done; err != nil {
		t.Fatalf("supervisor: %v", err)
	}
}

func TestReplacementsBackOffUntilADaemonStaysHealthy(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		healthyFor, termGrace time.Duration
		intermittent          bool
		reset                 bool
	}{
		{"sustained health", 40 * time.Millisecond, time.Millisecond, false, true},
		{"slow termination", 5 * time.Millisecond, 30 * time.Millisecond, false, false},
		{"intermittent failures", 40 * time.Millisecond, time.Millisecond, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var childNumber int
				var readyAt, stoppedAt time.Time
				var waits []time.Duration
				err := Loop(t.Context(), Config{
					ProbeInterval: time.Millisecond, ProbeTimeout: time.Millisecond,
					FailureLimit: 2, TermGrace: tc.termGrace, StartupTimeout: 5 * time.Millisecond,
					RestartBackoff: time.Millisecond, MaxRestartBackoff: 20 * time.Millisecond,
				}, Deps{
					Start: func() (Child, error) {
						childNumber++
						child := &timedSupervisorChild{supervisorTestChild: newSupervisorTestChild(), stoppedAt: &stoppedAt}
						if childNumber > 1 {
							waits = append(waits, time.Since(stoppedAt))
						}
						if childNumber == 9 {
							child.done <- nil
						}
						return child, nil
					},
					Probe: func(context.Context) error {
						if childNumber == 2 {
							if readyAt.IsZero() {
								readyAt = time.Now()
							}
							elapsed := time.Since(readyAt)
							if elapsed < tc.healthyFor && (!tc.intermittent || elapsed%(4*time.Millisecond) != time.Millisecond) {
								return nil
							}
						}
						return errors.New("unresponsive")
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				want := []time.Duration{1, 2, 4, 8, 16, 20, 20, 20}
				if tc.reset {
					want = []time.Duration{1, 1, 2, 4, 8, 16, 20, 20}
				}
				for i := range want {
					want[i] *= time.Millisecond
				}
				if !slices.Equal(waits, want) {
					t.Fatalf("replacement delays = %v, want %v", waits, want)
				}
			})
		})
	}
}

type timedSupervisorChild struct {
	*supervisorTestChild
	stoppedAt *time.Time
}

func (c *timedSupervisorChild) Kill() error {
	*c.stoppedAt = time.Now()
	return c.supervisorTestChild.Kill()
}

func TestSupervisorCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		starts := 0
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		err := Loop(ctx, Config{
			ProbeInterval: time.Millisecond, ProbeTimeout: time.Millisecond,
			FailureLimit: 1, TermGrace: time.Millisecond, StartupTimeout: time.Millisecond,
			RestartBackoff: time.Second, MaxRestartBackoff: time.Second,
		}, Deps{
			Start: func() (Child, error) {
				starts++
				return newSupervisorTestChild(), nil
			},
			Probe: func(context.Context) error { return errors.New("unresponsive") },
		})
		if err != nil || starts != 1 {
			t.Fatalf("cancel during backoff: starts=%d, err=%v", starts, err)
		}
	})
}
