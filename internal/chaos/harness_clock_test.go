package chaos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	clocktest "k8s.io/utils/clock/testing"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestHarnessSettleWindowsFollowItsClock(t *testing.T) {
	clock := clocktest.NewFakeClock(time.Unix(1_700_000_000, 0))
	h := &harness{
		cfg: config{Settle: time.Second, DaemonGraceMS: 100}, timeSource: clock,
		daemonKilledAt: clock.Now(),
		actors:         map[string]*actor{"finished": {exited: true, exitedAt: clock.Now()}},
	}
	live, known := h.processSets()
	if !live["finished"] || !known["finished"] || h.leakStable() {
		t.Fatal("fresh exits did not retain their settle windows")
	}
	clock.Step(time.Second)
	live, known = h.processSets()
	if live["finished"] || !known["finished"] || h.leakStable() {
		t.Fatal("actor settle time and daemon grace did not remain independent")
	}
	clock.Step(100*time.Millisecond + time.Nanosecond)
	if !h.leakStable() {
		t.Fatal("daemon grace did not end on the injected clock")
	}
}

func TestResidentProbeChargesAResidentAfterItsClockDeadline(t *testing.T) {
	clock := clocktest.NewFakeClock(time.Unix(1_700_000_000, 0))
	started := clock.Now()
	h := &harness{
		cfg: config{Settle: 50 * time.Millisecond}, timeSource: clock,
		residentReader: func(string) ([]int, error) { return []int{4242}, nil },
	}
	violations := h.homeResidentViolations()
	if len(violations) != 1 || !strings.Contains(violations[0], "4242") {
		t.Fatalf("resident after deadline: %v", violations)
	}
	if clock.Since(started) < h.cfg.Settle {
		t.Fatal("resident was charged before its settle window ended")
	}
}

func TestProcessPollingUsesItsClockAndCapsTheLastDelay(t *testing.T) {
	clock := clocktest.NewFakeClock(time.Unix(1_700_000_000, 0))
	started := clock.Now()
	calls := 0
	if !pollProcessState(clock, time.Second, 20*time.Millisecond, func() bool { calls++; return calls == 3 }) {
		t.Fatal("process condition was not observed")
	}
	if clock.Since(started) != 40*time.Millisecond {
		t.Fatal("process probes did not use the injected pacing")
	}
	started = clock.Now()
	if pollProcessState(clock, 25*time.Millisecond, 20*time.Millisecond, func() bool { return false }) {
		t.Fatal("an unmet process condition passed")
	}
	if clock.Since(started) != 25*time.Millisecond {
		t.Fatal("the final process-probe delay exceeded its deadline")
	}
}

func TestProcessGuardTicksAndDatesZombiesWithItsClock(t *testing.T) {
	requireProcessGroups(t)
	group, err := procgroup.Start(helperCommand("hang", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := group.Terminate(ctx, 20*time.Millisecond)
		if errors.Is(err, procgroup.ErrCleanup) || !group.Reaped() {
			t.Errorf("clock fixture cleanup: %v", err)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		clock := clocktest.NewFakeClock(time.Unix(1_700_000_000, 0))
		fired := make(chan []string, 1)
		h := &harness{
			cfg:        config{MaxOwnedProcesses: 3, MaxZombieDrain: time.Second},
			timeSource: clock, guardInterval: time.Second,
			processReader: func() ([]procgroup.Info, error) {
				return []procgroup.Info{{PID: group.ID(), Group: group.ID(), State: "Z"}}, nil
			},
			processFailure: func(violations []string) { fired <- violations },
		}
		stop := h.startProcessGuard()
		defer stop()
		synctest.Wait()
		if !clock.HasWaiters() {
			t.Fatal("process guard did not arm the injected ticker")
		}
		h.mu.Lock()
		h.actors = map[string]*actor{"held": {group: group}}
		h.mu.Unlock()
		for range 2 {
			clock.Step(time.Second)
			synctest.Wait()
			select {
			case violations := <-fired:
				t.Fatalf("zombie was charged before its drain deadline: %v", violations)
			default:
			}
		}
		clock.Step(time.Second)
		synctest.Wait()
		select {
		case violations := <-fired:
			if len(violations) != 1 || !strings.Contains(violations[0], "stayed unreaped") {
				t.Fatalf("zombie deadline: %v", violations)
			}
		default:
			t.Fatal("periodic process guard did not charge the expired zombie")
		}
	})
}
