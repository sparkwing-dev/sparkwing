package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type supervisorTestChild struct {
	mu    sync.Mutex
	done  chan error
	terms int
	kills int
}

func newSupervisorTestChild() *supervisorTestChild {
	return &supervisorTestChild{done: make(chan error, 1)}
}

func (c *supervisorTestChild) Wait() <-chan error { return c.done }

func (c *supervisorTestChild) Terminate() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terms++
	return nil
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
	var starts int
	startedSuccessor := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- superviseWingd(ctx, wingdSupervisorConfig{
			ProbeInterval: time.Millisecond,
			ProbeTimeout:  time.Millisecond,
			FailureLimit:  2,
			TermGrace:     time.Millisecond,
		}, wingdSupervisorDeps{
			Start: func() (wingdSupervisedChild, error) {
				if starts >= len(children) {
					t.Fatalf("started more than one successor")
				}
				child := children[starts]
				starts++
				if starts == 2 {
					close(startedSuccessor)
				}
				return child, nil
			},
			Probe: func(context.Context) error { return errors.New("unresponsive") },
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
	if starts != 2 {
		t.Fatalf("starts = %d, want predecessor plus exactly one successor", starts)
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
	err := superviseWingd(context.Background(), wingdSupervisorConfig{
		ProbeInterval: time.Hour,
		ProbeTimeout:  time.Millisecond,
		FailureLimit:  2,
		TermGrace:     time.Millisecond,
	}, wingdSupervisorDeps{
		Start: func() (wingdSupervisedChild, error) {
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
