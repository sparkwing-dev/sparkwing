package supervise

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAdvancingHeartbeatDefersReplacementUntilFailureCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newSupervisorTestChild()
		second := newSupervisorTestChild()
		second.done <- nil
		var starts, heartbeat atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- Loop(t.Context(), Config{
				ProbeInterval: time.Millisecond, ProbeTimeout: time.Millisecond,
				FailureLimit: 2, TermGrace: time.Millisecond, StartupTimeout: time.Millisecond,
				HeartbeatStale: 5 * time.Millisecond, FailureCeiling: 25 * time.Millisecond,
				RestartBackoff: time.Millisecond, MaxRestartBackoff: time.Millisecond,
			}, Deps{
				Start: func() (Child, error) {
					if starts.Add(1) == 1 {
						return first, nil
					}
					return second, nil
				},
				Probe:     func(context.Context) error { return errors.New("overloaded") },
				Heartbeat: func() (uint64, error) { return uint64(heartbeat.Add(1)), nil },
			})
		}()
		time.Sleep(20 * time.Millisecond)
		synctest.Wait()
		if terms, _ := first.actions(); terms != 0 || starts.Load() != 1 {
			t.Fatalf("advancing heartbeat replaced daemon before ceiling: terms %d, starts %d", terms, starts.Load())
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if terms, _ := first.actions(); terms != 1 || starts.Load() != 2 {
			t.Fatalf("failure ceiling did not replace daemon: terms %d, starts %d", terms, starts.Load())
		}
	})
}

func TestStaleHeartbeatAndFailedProbesReplaceDaemon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newSupervisorTestChild()
		second := newSupervisorTestChild()
		second.done <- nil
		var mu sync.Mutex
		mu.Lock()
		starts, probes := 0, 0
		var replacement map[string]any
		var failureStarted bool
		dumpPath := filepath.Join(t.TempDir(), "dump.txt")
		err := Loop(t.Context(), Config{
			ProbeInterval: time.Millisecond, ProbeTimeout: time.Millisecond,
			FailureLimit: 2, TermGrace: time.Millisecond, StartupTimeout: time.Millisecond,
			HeartbeatStale: 5 * time.Millisecond, FailureCeiling: time.Minute,
			RestartBackoff: time.Millisecond, MaxRestartBackoff: time.Millisecond,
		}, Deps{
			Start: func() (Child, error) {
				starts++
				if starts == 1 {
					return first, nil
				}
				return second, nil
			},
			Probe: func(context.Context) error {
				probes++
				if probes == 1 {
					return nil
				}
				return errors.New("blocked")
			},
			Heartbeat: func() (uint64, error) {
				if !mu.TryLock() {
					return 0, nil
				}
				mu.Unlock()
				return 1, nil
			},
			CaptureDump: func(_ context.Context, child Child) (string, error) {
				if child != first {
					t.Errorf("captured a successor's dump")
				}
				return dumpPath, os.WriteFile(dumpPath, []byte("goroutine 1"), 0o600)
			},
			Journal: func(kind string, data map[string]any) {
				if kind == "probe_failure_start" {
					failureStarted = true
				}
				if kind == "replacement" {
					replacement = data
				}
			},
		})
		mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if terms, _ := first.actions(); terms != 1 || starts != 2 {
			t.Fatalf("stale heartbeat did not replace daemon: terms %d, starts %d", terms, starts)
		}
		if !failureStarted || replacement["failed_probes"] == nil || replacement["dump_path"] != dumpPath {
			t.Fatalf("replacement evidence: start=%t record=%+v", failureStarted, replacement)
		}
		if body, err := os.ReadFile(dumpPath); err != nil || string(body) != "goroutine 1" {
			t.Fatalf("dump: %q %v", body, err)
		}
	})
}

func TestMachineStallDoesNotReplaceProgressingDaemon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child := newSupervisorTestChild()
		var probes, counter int
		starts := 0
		err := Loop(t.Context(), Config{
			ProbeInterval: 2 * time.Second, ProbeTimeout: 3 * time.Second,
			FailureLimit: 3, TermGrace: 15 * time.Second, StartupTimeout: 30 * time.Second,
			HeartbeatStale: time.Minute, FailureCeiling: 5 * time.Minute,
			RestartBackoff: time.Second, MaxRestartBackoff: 30 * time.Second,
		}, Deps{
			Start: func() (Child, error) {
				starts++
				return child, nil
			},
			Probe: func(context.Context) error {
				probes++
				if probes == 8 {
					time.Sleep(61 * time.Second)
				}
				if probes == 13 {
					if terms, _ := child.actions(); terms != 0 || starts != 1 {
						t.Errorf("machine stall replaced progressing daemon: terms %d, starts %d", terms, starts)
					}
					child.done <- nil
					return nil
				}
				if probes <= 5 {
					return nil
				}
				return errors.New("overloaded")
			},
			Heartbeat: func() (uint64, error) {
				if probes != 8 {
					counter++
				}
				return uint64(counter), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestCounterChangeCountsAsProgressAfterLongUptime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child := newSupervisorTestChild()
		var probes, counter int
		err := Loop(t.Context(), Config{
			ProbeInterval: time.Second, ProbeTimeout: time.Second,
			FailureLimit: 3, TermGrace: time.Second, StartupTimeout: time.Second,
			HeartbeatStale: 5 * time.Second, FailureCeiling: time.Minute,
			RestartBackoff: time.Second, MaxRestartBackoff: time.Second,
		}, Deps{
			Start: func() (Child, error) { return child, nil },
			Probe: func(context.Context) error {
				probes++
				if probes == 75 {
					if terms, _ := child.actions(); terms != 0 {
						t.Errorf("changing counter replaced daemon after %d probes", probes)
					}
					child.done <- nil
					return nil
				}
				if probes <= 60 {
					return nil
				}
				return errors.New("overloaded")
			},
			Heartbeat: func() (uint64, error) {
				if probes == 69 {
					counter = 0
				}
				counter++
				return uint64(counter), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestOneStaleSampleDoesNotReplaceDaemon(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child := newSupervisorTestChild()
		probes := 0
		err := Loop(t.Context(), Config{
			ProbeInterval: time.Millisecond, ProbeTimeout: time.Millisecond,
			FailureLimit: 2, TermGrace: time.Millisecond, StartupTimeout: time.Millisecond,
			HeartbeatStale: 3 * time.Millisecond, FailureCeiling: time.Minute,
			RestartBackoff: time.Millisecond, MaxRestartBackoff: time.Millisecond,
		}, Deps{
			Start: func() (Child, error) { return child, nil },
			Probe: func(context.Context) error {
				probes++
				if probes == 9 {
					if terms, _ := child.actions(); terms != 0 {
						t.Errorf("one stale sample replaced daemon: terms %d", terms)
					}
					child.done <- nil
					return nil
				}
				if probes == 1 {
					return nil
				}
				return errors.New("overloaded")
			},
			Heartbeat: func() (uint64, error) {
				if probes >= 7 {
					return 3, nil
				}
				if probes >= 3 {
					return 2, nil
				}
				return 1, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestFirstFailureAfterLongHealthMeasuresStalenessFromLastSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		child := newSupervisorTestChild()
		probes := 0
		err := Loop(t.Context(), Config{
			ProbeInterval: time.Second, ProbeTimeout: time.Second,
			FailureLimit: 3, TermGrace: time.Second, StartupTimeout: time.Second,
			HeartbeatStale: 5 * time.Second, FailureCeiling: time.Minute,
			RestartBackoff: time.Second, MaxRestartBackoff: time.Second,
		}, Deps{
			Start: func() (Child, error) { return child, nil },
			Probe: func(context.Context) error {
				probes++
				if probes == 110 {
					if terms, _ := child.actions(); terms != 0 {
						t.Errorf("a 4s stall after 100s of health replaced the daemon: terms %d", terms)
					}
					child.done <- nil
					return nil
				}
				if probes > 100 && probes <= 104 {
					return errors.New("overloaded")
				}
				return nil
			},
			Heartbeat: func() (uint64, error) { return 1, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestWedgedDaemonWithSlowProbesStillHitsFailureCeiling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		first := newSupervisorTestChild()
		second := newSupervisorTestChild()
		second.done <- nil
		var starts atomic.Int32
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Minute)
		defer cancel()
		err := Loop(ctx, Config{
			ProbeInterval: time.Second, ProbeTimeout: time.Second,
			FailureLimit: 3, TermGrace: time.Second, StartupTimeout: time.Second,
			HeartbeatStale: 5 * time.Second, FailureCeiling: time.Minute,
			RestartBackoff: time.Second, MaxRestartBackoff: time.Second,
		}, Deps{
			Start: func() (Child, error) {
				if starts.Add(1) == 1 {
					return first, nil
				}
				return second, nil
			},
			Probe: func(context.Context) error {
				time.Sleep(11 * time.Second)
				return errors.New("overloaded")
			},
			Heartbeat: func() (uint64, error) { return 1, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if starts.Load() != 2 {
			t.Fatalf("slow probes kept a wedged daemon past the ceiling: starts %d", starts.Load())
		}
	})
}
