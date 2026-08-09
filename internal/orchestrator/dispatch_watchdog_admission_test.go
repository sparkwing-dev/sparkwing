package orchestrator

import (
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
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

func TestDispatchWatchdog_WakesWhenWedgedSiblingStarts(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	waits := newAdmissionWaitTracker()
	state := &dispatchState{
		starts:         map[string]time.Time{"queued": time.Now()},
		outcomes:       map[string]sparkwing.Outcome{},
		durations:      map[string]time.Duration{},
		admissionWaits: waits,
	}
	waits.begin("queued")
	defer func() {
		waits.end("queued")
		wg.Done()
		wg.Done()
	}()

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 40*time.Millisecond, waits, state.watchdogActiveNodeIDs)
	}()
	time.Sleep(80 * time.Millisecond)
	state.markStarted("wedged")

	select {
	case got := <-result:
		if got != dispatchWaitTimedOut {
			t.Fatalf("watchdog result = %v, want dispatchWaitTimedOut", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("newly started wedged sibling did not wake the paused watchdog")
	}
}

func TestDispatchWatchdog_PausesWhenRunningSiblingFinishes(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	waits := newAdmissionWaitTracker()
	state := &dispatchState{
		starts: map[string]time.Time{
			"queued":  time.Now(),
			"running": time.Now(),
		},
		outcomes:       map[string]sparkwing.Outcome{},
		durations:      map[string]time.Duration{},
		admissionWaits: waits,
	}
	waits.begin("queued")

	result := make(chan dispatchWaitResult, 1)
	go func() {
		result <- waitForDispatch(&wg, 80*time.Millisecond, waits, state.watchdogActiveNodeIDs)
	}()
	time.Sleep(20 * time.Millisecond)
	state.setOutcome("running", sparkwing.Success)

	select {
	case got := <-result:
		t.Fatalf("watchdog returned %v after only the admission waiter remained", got)
	case <-time.After(140 * time.Millisecond):
	}

	waits.end("queued")
	state.setOutcome("queued", sparkwing.Success)
	wg.Done()
	select {
	case got := <-result:
		if got != dispatchWaitDone {
			t.Fatalf("watchdog result = %v, want dispatchWaitDone", got)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not finish after queued work completed")
	}
}
