package orchestrator

import (
	"sync"
	"testing"
	"time"
)

func TestDispatchWatchdog_PausesWhileNodeWaitsForAdmission(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	waits := newAdmissionWaitTracker()
	waits.begin()

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits)
	}()

	select {
	case got := <-result:
		t.Fatalf("watchdog returned %v while admission was still pending", got)
	case <-time.After(120 * time.Millisecond):
	}

	waits.end()
	wg.Done()
	select {
	case got := <-result:
		if got != dispatchWaitDone {
			t.Fatalf("watchdog result = %v, want dispatchWaitDone", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not finish after admission and dispatch completed")
	}
}

func TestDispatchWatchdog_ArmsAfterAdmissionWaitEnds(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	waits := newAdmissionWaitTracker()
	waits.begin()

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits)
	}()
	time.Sleep(80 * time.Millisecond)
	waits.end()

	select {
	case got := <-result:
		if got != dispatchWaitTimedOut {
			t.Fatalf("watchdog result = %v, want dispatchWaitTimedOut", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not arm after admission wait ended")
	}
	wg.Done()
}
