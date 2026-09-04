package supervise

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type supervisorTestChild struct {
	mu      sync.Mutex
	done    chan error
	terms   int
	kills   int
	termErr error
}

type nonExitingSupervisorChild struct {
	done   chan error
	killed chan struct{}
}

func newNonExitingSupervisorChild() *nonExitingSupervisorChild {
	return &nonExitingSupervisorChild{done: make(chan error, 1), killed: make(chan struct{})}
}

func (c *nonExitingSupervisorChild) Wait() <-chan error { return c.done }
func (c *nonExitingSupervisorChild) Terminate() error   { return nil }
func (c *nonExitingSupervisorChild) Kill() error {
	close(c.killed)
	return nil
}

func newSupervisorTestChild() *supervisorTestChild {
	return &supervisorTestChild{done: make(chan error, 1)}
}

func (c *supervisorTestChild) Wait() <-chan error { return c.done }

func (c *supervisorTestChild) Terminate() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terms++
	return c.termErr
}

func (c *supervisorTestChild) Kill() error {
	c.mu.Lock()
	c.kills++
	c.mu.Unlock()
	c.done <- errors.New("killed")
	return nil
}

func (c *supervisorTestChild) actions() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.terms, c.kills
}

func TestWingdSupervisorHardStopsOnlyAfterBoundedTermAndStartsOneSuccessor(t *testing.T) {
	wedged := newSupervisorTestChild()
	successor := newSupervisorTestChild()
	children := []*supervisorTestChild{wedged, successor}
	var starts atomic.Int32
	startedSuccessor := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Loop(ctx, Config{
			ProbeInterval: time.Millisecond,
			ProbeTimeout:  time.Millisecond,
			FailureLimit:  2,
			TermGrace:     time.Millisecond,
		}, Deps{
			Start: func() (Child, error) {
				index := int(starts.Add(1) - 1)
				if index >= len(children) {
					t.Errorf("started more than one successor")
					return nil, errors.New("too many starts")
				}
				child := children[index]
				if index == 1 {
					close(startedSuccessor)
				}
				return child, nil
			},
			Probe: func(context.Context) error {
				if starts.Load() == 1 {
					return errors.New("unresponsive")
				}
				return nil
			},
		})
	}()

	select {
	case <-startedSuccessor:
	case <-time.After(time.Second):
		t.Fatal("TERM-resistant child was not replaced within the watchdog bound")
	}
	terms, kills := wedged.actions()
	if terms != 1 {
		t.Fatalf("wedged child terminations = %d, want one graceful termination", terms)
	}
	if kills != 1 {
		t.Fatalf("wedged child kills = %d, want one hard stop after grace", kills)
	}
	if starts.Load() != 2 {
		t.Fatalf("starts = %d, want predecessor plus exactly one successor", starts.Load())
	}

	cancel()
	successor.done <- nil
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop after cancellation")
	}
}

func TestWingdSupervisorDoesNotRestartAChildThatExitsWithoutWatchdogRecovery(t *testing.T) {
	child := newSupervisorTestChild()
	child.done <- nil
	starts := 0
	err := Loop(context.Background(), Config{
		ProbeInterval: time.Hour,
		ProbeTimeout:  time.Millisecond,
		FailureLimit:  2,
		TermGrace:     time.Millisecond,
	}, Deps{
		Start: func() (Child, error) {
			starts++
			return child, nil
		},
		Probe: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("supervise clean child exit: %v", err)
	}
	if starts != 1 {
		t.Fatalf("clean idle/drain exit started %d children, want one", starts)
	}
}

func TestStopChildBoundsPostKillWait(t *testing.T) {
	child := newNonExitingSupervisorChild()
	t.Cleanup(func() {
		select {
		case child.done <- nil:
		default:
		}
	})

	result := make(chan error, 1)
	go func() { result <- stopChild(child, 20*time.Millisecond) }()
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "did not exit after kill") {
			t.Fatalf("post-kill wait error = %v, want bounded exit failure", err)
		}
	case <-timer.C:
		t.Fatal("stopChild blocked after Kill returned without child exit")
	}
	select {
	case <-child.killed:
	default:
		t.Fatal("stopChild returned without escalating to Kill")
	}
}

func TestStopChildAcceptsExitDuringPostKillWait(t *testing.T) {
	child := newNonExitingSupervisorChild()
	t.Cleanup(func() {
		select {
		case child.done <- nil:
		default:
		}
	})

	result := make(chan error, 1)
	go func() { result <- stopChild(child, 20*time.Millisecond) }()
	escalationTimer := time.NewTimer(500 * time.Millisecond)
	defer escalationTimer.Stop()
	select {
	case <-child.killed:
	case <-escalationTimer.C:
		t.Fatal("stopChild did not escalate to Kill")
	}
	child.done <- nil
	completionTimer := time.NewTimer(500 * time.Millisecond)
	defer completionTimer.Stop()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("post-kill child exit returned %v, want success", err)
		}
	case <-completionTimer.C:
		t.Fatal("stopChild did not accept child exit during post-kill grace")
	}
}

func TestStopChildEscalatesWhenTerminateReturnsError(t *testing.T) {
	child := newSupervisorTestChild()
	child.termErr = errors.New("signal refused")

	if err := stopChild(child, 20*time.Millisecond); err != nil {
		t.Fatalf("forced cleanup after terminate error: %v", err)
	}
	terms, kills := child.actions()
	if terms != 1 || kills != 1 {
		t.Fatalf("cleanup actions after terminate error = term:%d kill:%d, want one each", terms, kills)
	}
}

func TestStopChildDoesNotSignalAChildThatAlreadyExited(t *testing.T) {
	child := newSupervisorTestChild()
	want := errors.New("idle exit")
	child.done <- want

	if err := stopChild(child, time.Second); !errors.Is(err, want) {
		t.Fatalf("stopChild = %v, want the recorded exit %v", err, want)
	}
	terms, kills := child.actions()
	if terms != 0 || kills != 0 {
		t.Fatalf("signals sent to an exited child = term:%d kill:%d, want none", terms, kills)
	}
}

func TestExecChildStopsSignallingOnceItHasBeenReaped(t *testing.T) {
	child, err := startExecChild(os.Args[0], []string{"-test.list=^$"})
	if err != nil {
		t.Fatalf("start exec child: %v", err)
	}
	select {
	case <-child.Wait():
	case <-time.After(30 * time.Second):
		t.Fatal("helper process never exited")
	}

	if err := child.Terminate(); err != nil {
		t.Fatalf("terminate after reap = %v, want a no-op", err)
	}
	if err := child.Kill(); err != nil {
		t.Fatalf("kill after reap = %v, want a no-op", err)
	}
}
