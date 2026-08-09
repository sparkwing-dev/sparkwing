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
	waits.begin("queued")

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits, func() []string { return []string{"queued"} })
	}()

	select {
	case got := <-result:
		t.Fatalf("watchdog returned %v while admission was still pending", got)
	case <-time.After(120 * time.Millisecond):
	}

	waits.end("queued")
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
	waits.begin("queued")

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits, func() []string { return []string{"queued"} })
	}()
	time.Sleep(80 * time.Millisecond)
	waits.end("queued")

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

func TestDispatchWatchdog_QueuedNodeDoesNotMaskWedgedSibling(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	waits := newAdmissionWaitTracker()
	waits.begin("queued")
	defer func() {
		waits.end("queued")
		wg.Done()
		wg.Done()
	}()

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits, func() []string {
			return []string{"queued", "wedged"}
		})
	}()

	select {
	case got := <-result:
		if got != dispatchWaitTimedOut {
			t.Fatalf("watchdog result = %v, want dispatchWaitTimedOut", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("queued node masked a wedged sibling")
	}
}
