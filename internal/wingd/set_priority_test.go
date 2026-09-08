package wingd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func setPriority(t *testing.T, home, runID string, priority int, mode string) wingwire.SetPriorityAck {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl := ensure(t, home, "")
	ack, err := cl.SetPriority(ctx, runID, priority, mode)
	if err != nil {
		t.Fatalf("set priority %s: %v", runID, err)
	}
	return ack
}

// A node waiter renders under its owning run, so it is found by participant id.
func waiterPriority(qs wingwire.QueueState, id string) (int, bool) {
	for _, w := range qs.Waiters {
		if w.RunID == id || w.ParticipantID == id {
			return w.Priority, true
		}
	}
	return 0, false
}

func waitForParticipant(t *testing.T, home, participantID string) wingwire.QueueState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		qs := queueOnce(t, home)
		if _, ok := waiterPriority(qs, participantID); ok {
			return qs
		}
		if time.Now().After(deadline) {
			t.Fatalf("participant %q never appeared as a waiter: %+v", participantID, qs.Waiters)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSetPriority_WaiterIsReRankedAndAdmitted(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	hog := ensure(t, home, "")
	hogLease := mustAcquire(t, hog, coreReq("hog", 3))

	first := ensure(t, home, "")
	firstPositions, firstResult := acquireAsync(first, coreReq("first", 3))
	waitForQueue(t, firstPositions)
	second := ensure(t, home, "")
	secondPositions, secondResult := acquireAsync(second, coreReq("second", 1))
	waitForQueue(t, secondPositions)

	ack := setPriority(t, home, "second", 5, "")
	if !ack.Found || ack.Priority != 5 || ack.Previous != 0 {
		t.Fatalf("ack = %+v, want found at 5 from 0", ack)
	}
	if ack.Holding {
		t.Fatalf("ack = %+v, want a queued run rather than a holder", ack)
	}
	if ack.Participants != 1 {
		t.Fatalf("ack participants = %d, want 1", ack.Participants)
	}

	if err := hogLease.Release(); err != nil {
		t.Fatalf("release hog: %v", err)
	}
	if r := waitResult(t, secondResult, 3*time.Second); r.err != nil {
		t.Fatalf("second never admitted after the raise: %v", r.err)
	}
	qs := waitForWaiter(t, home, "first")
	if _, still := waiterPriority(qs, "second"); still {
		t.Fatalf("second is still queued after outranking first: %+v", qs.Waiters)
	}
	if len(qs.Waiters) != 1 {
		t.Fatalf("waiters = %+v, want only the run second overtook", qs.Waiters)
	}
	_ = firstResult
}

func TestSetPriority_HolderReportsThatOnlyItsLaterNodesMove(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("holder-run", 1))
	defer func() { _ = lease.Release() }()

	ack := setPriority(t, home, "holder-run", 6, "")
	if !ack.Found || !ack.Holding {
		t.Fatalf("ack = %+v, want found and holding", ack)
	}
	if ack.Position != 0 {
		t.Fatalf("ack position = %d, want 0: nothing of the run is queued", ack.Position)
	}
	if ack.Priority != 6 {
		t.Fatalf("ack priority = %d, want 6", ack.Priority)
	}
}

func TestSetPriority_UnknownRunIsReportedNotFound(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, GraceWindow: -1})

	ack := setPriority(t, home, "never-heard-of-it", 3, "")
	if ack.Found {
		t.Fatalf("ack = %+v, want not found", ack)
	}
	if ack.Reason != "not in local admission" {
		t.Fatalf("ack reason = %q, want the local-admission phrase", ack.Reason)
	}
}

func TestSetPriority_RelativeModesExcludeTheRunsOwnParticipants(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	hog := ensure(t, home, "")
	hogLease := mustAcquire(t, hog, coreReq("hog", 4))
	defer func() { _ = hogLease.Release() }()

	high := ensure(t, home, "")
	highReq := coreReq("high", 1)
	highReq.Priority = 4
	highPositions, _ := acquireAsync(high, highReq)
	waitForQueue(t, highPositions)

	low := ensure(t, home, "")
	lowReq := coreReq("low", 1)
	lowReq.Priority = -2
	lowPositions, _ := acquireAsync(low, lowReq)
	waitForQueue(t, lowPositions)

	mover := ensure(t, home, "")
	moverPositions, _ := acquireAsync(mover, coreReq("mover", 1))
	waitForQueue(t, moverPositions)

	if ack := setPriority(t, home, "mover", 0, "front"); ack.Priority != 5 || ack.Position != 1 {
		t.Fatalf("front ack = %+v, want priority 5 at position 1", ack)
	}
	// Front resolved against the others only: re-asking must not chase the
	// run's own new rank upward.
	if ack := setPriority(t, home, "mover", 0, "front"); ack.Priority != 5 {
		t.Fatalf("second front ack = %+v, want a stable 5", ack)
	}
	if ack := setPriority(t, home, "mover", 0, "back"); ack.Priority != -3 || ack.Previous != 5 {
		t.Fatalf("back ack = %+v, want priority -3 from 5", ack)
	}
	qs := waitForWaiter(t, home, "mover")
	if p, ok := waiterPriority(qs, "mover"); !ok || p != -3 {
		t.Fatalf("mover priority = %d (present %v), want -3", p, ok)
	}
}

func TestSetPriority_OverrideAppliesToASubsequentOwnerRequest(t *testing.T) {
	home := shortHome(t)
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	runCl := ensure(t, home, "")
	runLease := mustAcquire(t, runCl, coreReq("parent-run", 1))
	defer func() { _ = runLease.Release() }()
	hog := ensure(t, home, "")
	hogLease := mustAcquire(t, hog, coreReq("hog", 2))
	defer func() { _ = hogLease.Release() }()

	if ack := setPriority(t, home, "parent-run", 7, ""); !ack.Found {
		t.Fatalf("ack = %+v, want found", ack)
	}

	nodeCl := ensure(t, home, "")
	nodeReq := coreReq("node-1", 2)
	nodeReq.OwnerRunID = "parent-run"
	nodeReq.OwnerLeaseToken = runLease.Token
	nodePositions, _ := acquireAsync(nodeCl, nodeReq)
	waitForQueue(t, nodePositions)

	qs := waitForParticipant(t, home, "node-1")
	if p, ok := waiterPriority(qs, "node-1"); !ok || p != 7 {
		t.Fatalf("node priority = %d (present %v), want the run's 7 rather than the carried 0", p, ok)
	}
}

func TestSetPriority_OverrideIsPersistedForTheNextDaemon(t *testing.T) {
	home := shortHome(t)
	td := startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	holder := ensure(t, home, "")
	lease := mustAcquire(t, holder, coreReq("survivor", 1))
	if ack := setPriority(t, home, "survivor", 8, ""); !ack.Found {
		t.Fatalf("ack = %+v, want found", ack)
	}
	_ = lease

	td.stop()
	if err := td.waitExit(t, 5*time.Second); err != nil {
		t.Fatalf("daemon exit: %v", err)
	}

	var persisted struct {
		Snapshot admission.Snapshot `json:"snapshot"`
	}
	blob, err := os.ReadFile(filepath.Join(home, "wingd", "state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if err := json.Unmarshal(blob, &persisted); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	if got := persisted.Snapshot.PriorityOverrides["survivor"]; got != 8 {
		t.Fatalf("persisted overrides = %+v, want survivor at 8", persisted.Snapshot.PriorityOverrides)
	}
	if len(persisted.Snapshot.Waiters) != 0 {
		t.Fatalf("persisted waiters = %+v, want none: the override is what carries the rank across a restart", persisted.Snapshot.Waiters)
	}
}

func TestSetPriority_RestoredOverrideRanksTheRunsNextRequest(t *testing.T) {
	home := shortHome(t)
	writeLedgerState(t, home, marshalState(t, admission.Snapshot{
		TotalMilliCores:     4000,
		TotalMemoryBytes:    8 << 30,
		HeadroomMilliCores:  4000,
		HeadroomMemoryBytes: 8 << 30,
		PriorityOverrides:   map[string]int{"restored-run": 8},
	}))
	startDaemon(t, wingd.Config{Home: home, Sampler: newFakeSampler(4, 8<<30), GraceWindow: -1})

	hog := ensure(t, home, "")
	hogLease := mustAcquire(t, hog, coreReq("hog", 3))
	defer func() { _ = hogLease.Release() }()

	waiter := ensure(t, home, "")
	positions, _ := acquireAsync(waiter, coreReq("restored-run", 2))
	waitForQueue(t, positions)

	qs := waitForWaiter(t, home, "restored-run")
	if p, ok := waiterPriority(qs, "restored-run"); !ok || p != 8 {
		t.Fatalf("restored priority = %d (present %v), want the persisted 8 rather than the carried 0", p, ok)
	}
}
