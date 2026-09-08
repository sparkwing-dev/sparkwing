package admission

import (
	"encoding/json"
	"reflect"
	"testing"
)

func waiterOrder(l *Ledger) []string {
	snap := l.Snapshot()
	ids := make([]string, len(snap.Waiters))
	for i, w := range snap.Waiters {
		ids[i] = w.RequestID
	}
	return ids
}

// protectedHead queues an oversized waiter, lets a younger grant backfill past
// it so it becomes protected, and leaves free capacity a small waiter fits but
// may not take while the protection stands.
func protectedHead(t *testing.T, l *Ledger) {
	t.Helper()
	older := mustGrant(t, l, Request{ID: "older", MemoryBytes: 1 << 30})
	mustQueue(t, l, Request{ID: "heavy", MemoryBytes: 8 << 30})
	mustGrant(t, l, Request{ID: "younger", MemoryBytes: 6 << 30})
	mustRelease(t, l, older.ID, "older")
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].BackfillCount == 0 {
		t.Fatalf("waiters = %+v, want heavy queued and protected", snap.Waiters)
	}
}

func TestSetPriority_RaisePromotesImmediatelyWhenCapacityAllows(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	protectedHead(t, l)
	mustQueue(t, l, Request{ID: "small", MemoryBytes: 2 << 30})
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"heavy", "small"}) {
		t.Fatalf("waiters = %v, want small held behind heavy", got)
	}

	changed, events := l.SetPriority("small", 5)
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	wantKinds(t, events, EventReprioritized, EventPromoted)
	if events[0].RequestID != "small" || events[0].Priority != 5 {
		t.Fatalf("reprioritized event = %+v, want small at 5", events[0])
	}
	if events[1].RequestID != "small" {
		t.Fatalf("promoted %q, want small admitted the moment it outranks the head", events[1].RequestID)
	}
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"heavy"}) {
		t.Fatalf("waiters = %v, want only heavy", got)
	}
}

func TestSetPriority_LowerKeepsFIFOAmongEquals(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	mustGrant(t, l, Request{ID: "holder", MemoryBytes: 8 << 30})
	for _, id := range []string{"a", "b", "c"} {
		mustQueue(t, l, Request{ID: id, MemoryBytes: 4 << 30})
	}

	changed, events := l.SetPriority("a", -1)
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	wantKinds(t, events, EventReprioritized)
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"b", "c", "a"}) {
		t.Fatalf("waiters = %v, want b, c, a: the equals keep their arrival order", got)
	}

	if changed, _ := l.SetPriority("c", -1); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"b", "a", "c"}) {
		t.Fatalf("waiters = %v, want b, a, c: a was lowered first so it stays ahead of c", got)
	}
}

func TestSetPriority_OverrideAppliesToALaterOwnerScopedRequest(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	run := mustGrant(t, l, Request{ID: "run", MemoryBytes: 1 << 30})
	mustGrant(t, l, Request{ID: "filler", MemoryBytes: 6 << 30})
	mustQueue(t, l, Request{ID: "other", MemoryBytes: 4 << 30})

	if changed, _ := l.SetPriority("run", 9); changed != 0 {
		t.Fatalf("changed = %d, want 0: the run holds a lease and has no waiter yet", changed)
	}
	if got := l.EffectivePriority("node-1", "run", 0); got != 9 {
		t.Fatalf("EffectivePriority = %d, want 9", got)
	}

	mustQueue(t, l, Request{ID: "node-1", OwnerID: "run", MemoryBytes: 4 << 30})
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"node-1", "other"}) {
		t.Fatalf("waiters = %v, want the node ahead of other at its run's rank", got)
	}
	snap := l.Snapshot()
	if snap.Waiters[0].Priority != 9 {
		t.Fatalf("node priority = %d, want the run override 9 rather than the carried 0", snap.Waiters[0].Priority)
	}
	_ = run
}

func TestSetPriority_OverrideSurvivesSnapshotRestore(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	mustGrant(t, l, Request{ID: "run", MemoryBytes: 1 << 30})
	if _, _ = l.SetPriority("run", 4); l.Snapshot().PriorityOverrides["run"] != 4 {
		t.Fatalf("snapshot overrides = %+v, want run at 4", l.Snapshot().PriorityOverrides)
	}

	data, err := json.Marshal(l.Snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	restored, err := Restore(decoded, sequentialTokens())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.EffectivePriority("node-1", "run", 0); got != 4 {
		t.Fatalf("EffectivePriority after restore = %d, want 4", got)
	}
}

func TestSetPriority_OverrideDroppedWhenTheRunIsGone(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	lease := mustGrant(t, l, Request{ID: "run", MemoryBytes: 1 << 30})
	if _, _ = l.SetPriority("run", 4); len(l.Snapshot().PriorityOverrides) != 1 {
		t.Fatal("override not recorded")
	}

	mustRelease(t, l, lease.ID, "run")
	if got := l.Snapshot().PriorityOverrides; len(got) != 0 {
		t.Fatalf("overrides = %+v, want empty once the run holds and waits for nothing", got)
	}
	if got := l.EffectivePriority("node-1", "run", 2); got != 2 {
		t.Fatalf("EffectivePriority = %d, want the carried 2", got)
	}
}

func TestSetPriority_OverrideOutlivesAWaiterWhileTheRunStillHolds(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	run := mustGrant(t, l, Request{ID: "run", MemoryBytes: 1 << 30})
	mustGrant(t, l, Request{ID: "filler", MemoryBytes: 6 << 30})
	if _, _ = l.SetPriority("run", 3); len(l.Snapshot().PriorityOverrides) != 1 {
		t.Fatal("override not recorded")
	}
	mustQueue(t, l, Request{ID: "node-1", OwnerID: "run", MemoryBytes: 4 << 30})

	l.CancelWaiter("node-1")
	if got := l.Snapshot().PriorityOverrides; got["run"] != 3 {
		t.Fatalf("overrides = %+v, want the run's rank kept while it holds a lease", got)
	}
	_ = run
}

func TestSetPriority_RaisedRunStillAdmitsPastAProtectedWaiter(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	protectedHead(t, l)
	mustQueue(t, l, Request{ID: "light", MemoryBytes: 2 << 30})

	// The protection is what holds light back, so the raise has to beat it.
	changed, events := l.SetPriority("light", 1)
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	wantKinds(t, events, EventReprioritized, EventPromoted)
	if events[1].RequestID != "light" {
		t.Fatalf("promoted %q, want light: a raised run ahead of the protected waiter is admitted when it fits", events[1].RequestID)
	}
}

func TestSetPriority_LoweredRunKeepsItsOwnBackfillProtection(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	protectedHead(t, l)
	mustQueue(t, l, Request{ID: "peer", MemoryBytes: 8 << 30})

	if changed, _ := l.SetPriority("heavy", -1); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	if got := waiterOrder(l); !reflect.DeepEqual(got, []string{"peer", "heavy"}) {
		t.Fatalf("waiters = %v, want heavy behind peer", got)
	}
	snap := l.Snapshot()
	if snap.Waiters[1].BackfillCount == 0 {
		t.Fatalf("heavy backfill count = %d, want its protection kept across the demotion", snap.Waiters[1].BackfillCount)
	}

	d, _ := submit(t, l, Request{ID: "late", Priority: -1, MemoryBytes: 2 << 30})
	if d.Kind != DecisionQueued {
		t.Fatalf("late = %+v, want queued: the demoted waiter still reserves what it was protected for against its own rank", d)
	}
}

func TestSetPriority_ParticipantOverrideOutranksItsRuns(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	mustGrant(t, l, Request{ID: "run", MemoryBytes: 6 << 30})
	mustQueue(t, l, Request{ID: "node-1", OwnerID: "run", MemoryBytes: 4 << 30})

	if changed, _ := l.SetPriority("node-1", 8); changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}
	if changed, _ := l.SetPriority("run", 2); changed != 0 {
		t.Fatalf("changed = %d, want 0: the node carries a rank of its own", changed)
	}
	if snap := l.Snapshot(); snap.Waiters[0].Priority != 8 {
		t.Fatalf("node priority = %d, want its own 8", snap.Waiters[0].Priority)
	}
}

func TestSetPriority_UnknownRunRecordsARankForItsFutureParticipants(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	changed, events := l.SetPriority("nobody", 3)
	if changed != 0 || len(events) != 0 {
		t.Fatalf("SetPriority = (%d, %v), want no waiter touched", changed, events)
	}
	if got := l.EffectivePriority("nobody", "", 0); got != 3 {
		t.Fatalf("EffectivePriority = %d, want 3", got)
	}
}
