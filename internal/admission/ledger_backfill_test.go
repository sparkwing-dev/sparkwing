package admission

import "testing"

func TestWeighted_PromotionBackfillsPastNonFittingHeavyHead(t *testing.T) {
	l := testLedger(t, 0, 0)
	heavyHolder := mustGrant(t, l, Request{ID: "holder-heavy", Semaphores: []SemaphoreClaim{sem("k", 8, 6, PolicyQueue)}})
	lightHolder := mustGrant(t, l, Request{ID: "holder-light", Semaphores: []SemaphoreClaim{sem("k", 8, 2, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "heavy", Semaphores: []SemaphoreClaim{sem("k", 8, 6, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "light", Semaphores: []SemaphoreClaim{sem("k", 8, 2, PolicyQueue)}})
	_ = heavyHolder

	events := mustRelease(t, l, lightHolder.ID, "holder-light")
	wantKinds(t, events, EventReleased, EventBackfilled, EventPromoted)
	if events[1].RequestID != "heavy" || events[1].BypassedBy != "light" || events[1].BackfillCount != 1 {
		t.Fatalf("backfill event = %+v, want heavy bypassed once by light", events[1])
	}
	if events[2].RequestID != "light" {
		t.Fatalf("promoted %q, want light backfilling behind the non-fitting heavy head", events[2].RequestID)
	}
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "heavy" {
		t.Fatalf("waiters = %+v, want only heavy still queued", snap.Waiters)
	}
}

func TestWeighted_BackfillStopsWhenYoungerHolderBlocksHead(t *testing.T) {
	l := testLedger(t, 0, 0)
	older := mustGrant(t, l, Request{ID: "older", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "heavy", Semaphores: []SemaphoreClaim{sem("k", 10, 8, PolicyQueue)}})

	d, _ := submit(t, l, Request{ID: "light-1", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("light-1 = %+v, want granted as backfill while only the older holder blocks heavy", d)
	}
	light1 := d.Lease

	if pos := mustQueue(t, l, Request{ID: "light-2", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}}); pos < 0 {
		t.Fatalf("light-2 position = %d", pos)
	}

	events := mustRelease(t, l, older.ID, "older")
	wantKinds(t, events, EventReleased)
	if snap := l.Snapshot(); len(snap.Waiters) != 2 {
		t.Fatalf("waiters = %+v, want heavy and light-2 both still queued; light-2 must not jump the protected heavy", snap.Waiters)
	}

	events = mustRelease(t, l, light1.ID, "light-1")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "heavy" {
		t.Fatalf("promoted %q, want heavy once the younger holder releases", events[1].RequestID)
	}
}

func TestWeighted_HostMemoryBackfillPastHeavyHead(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	holder := mustGrant(t, l, Request{ID: "holder", MemoryBytes: 6 << 30})
	mustQueue(t, l, Request{ID: "heavy", MemoryBytes: 6 << 30})

	d, _ := submit(t, l, Request{ID: "light", MemoryBytes: 2 << 30})
	if d.Kind != DecisionGranted {
		t.Fatalf("light = %+v, want granted: free memory backfills past the heavy head blocked by the older holder", d)
	}
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "heavy" {
		t.Fatalf("waiters = %+v, want only heavy queued", snap.Waiters)
	}

	events := mustRelease(t, l, holder.ID, "holder")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "heavy" {
		t.Fatalf("promoted %q, want heavy once the older holder frees its memory", events[1].RequestID)
	}
}

func TestWeighted_OneBackfillProtectsOlderWaiterFromAStream(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	mustGrant(t, l, Request{ID: "guard", MemoryBytes: 1})
	if _, err := l.SetHeadroom(0, 5<<30); err != nil {
		t.Fatalf("set headroom: %v", err)
	}
	mustQueue(t, l, Request{ID: "heavy", MemoryBytes: 6 << 30})
	first := mustGrant(t, l, Request{ID: "light-1", MemoryBytes: 4 << 30})

	snap := l.Snapshot()
	if len(snap.Waiters) != 1 || snap.Waiters[0].BackfillCount != 1 {
		t.Fatalf("waiters = %+v, want heavy protected after one backfill", snap.Waiters)
	}
	mustRelease(t, l, first.ID, "light-1")
	l = restoreRoundTrip(t, l)
	if pos := mustQueue(t, l, Request{ID: "light-2", MemoryBytes: 4 << 30}); pos != 1 {
		t.Fatalf("light-2 position = %d, want behind protected heavy", pos)
	}

	events, err := l.SetHeadroom(0, 8<<30)
	if err != nil {
		t.Fatalf("restore headroom: %v", err)
	}
	wantKinds(t, events, EventPromoted)
	if events[0].RequestID != "heavy" {
		t.Fatalf("promoted %q, want protected heavy", events[0].RequestID)
	}
}

func TestWeighted_ProtectedWaiterAllowsBackfillInsideReservedCapacity(t *testing.T) {
	l := testLedger(t, 11, 0)
	older := mustGrant(t, l, Request{ID: "older", Cores: 7})
	mustQueue(t, l, Request{ID: "protected", Cores: 7})
	first := mustGrant(t, l, Request{ID: "light-1", Cores: 2})
	mustRelease(t, l, first.ID, "light-1")

	second := mustGrant(t, l, Request{ID: "light-2", Cores: 2})
	events := mustRelease(t, l, older.ID, "older")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "protected" {
		t.Fatalf("promoted %q, want protected while light-2 still holds reserved spare capacity", events[1].RequestID)
	}
	_ = second
}

func TestWeighted_QueuedBackfillProtectsOlderWaiter(t *testing.T) {
	l := testLedger(t, 0, 8<<30)
	mustGrant(t, l, Request{ID: "guard", MemoryBytes: 1})
	if _, err := l.SetHeadroom(0, 0); err != nil {
		t.Fatalf("clear headroom: %v", err)
	}
	mustQueue(t, l, Request{ID: "heavy", MemoryBytes: 6 << 30})
	mustQueue(t, l, Request{ID: "light", MemoryBytes: 4 << 30})

	events, err := l.SetHeadroom(0, 5<<30)
	if err != nil {
		t.Fatalf("raise headroom: %v", err)
	}
	wantKinds(t, events, EventBackfilled, EventPromoted)
	if events[0].RequestID != "heavy" || events[0].BypassedBy != "light" || events[0].BackfillCount != 1 {
		t.Fatalf("backfill event = %+v, want heavy bypassed once by light", events[0])
	}
	if events[1].RequestID != "light" {
		t.Fatalf("promoted %q, want one light backfill", events[1].RequestID)
	}
	if got := l.Snapshot().Waiters[0].BackfillCount; got != 1 {
		t.Fatalf("heavy backfill count = %d, want 1", got)
	}
}

func TestPriority_PromotesHighPriorityWaiterBeforeLowerPriorityQueue(t *testing.T) {
	l := testLedger(t, 8, 0)
	holder := mustGrant(t, l, Request{ID: "holder", Cores: 8})
	mustQueue(t, l, Request{ID: "push-1", Cores: 1})
	mustQueue(t, l, Request{ID: "push-2", Cores: 1})
	landPos := mustQueue(t, l, Request{ID: "land", Cores: 1, Priority: 100})
	if landPos != 0 {
		t.Fatalf("land position = %d, want 0 ahead despite lower-priority waiters", landPos)
	}

	events := mustRelease(t, l, holder.ID, "holder")
	wantKinds(t, events, EventReleased, EventPromoted, EventPromoted, EventPromoted)
	if events[1].RequestID != "land" {
		t.Fatalf("first promoted %q, want high-priority land", events[1].RequestID)
	}
}

func TestOwnerAdmissionRankOrdersEqualPriorityDescendants(t *testing.T) {
	l := testLedger(t, 1, 0)
	older := mustGrant(t, l, Request{ID: "owner-older"})
	newer := mustGrant(t, l, Request{ID: "owner-newer"})
	holder := mustGrant(t, l, Request{ID: "holder", Cores: 1})

	if pos := mustQueue(t, l, Request{ID: "newer-child", OwnerID: "owner-newer", Cores: 1}); pos != 0 {
		t.Fatalf("newer child position = %d, want 0", pos)
	}
	if pos := mustQueue(t, l, Request{ID: "older-child", OwnerID: "owner-older", Cores: 1}); pos != 0 {
		t.Fatalf("older child position = %d, want 0 ahead of the newer owner", pos)
	}
	if pos := mustQueue(t, l, Request{ID: "older-child-2", OwnerID: "owner-older", Cores: 1}); pos != 1 {
		t.Fatalf("second older child position = %d, want 1 behind its sibling", pos)
	}

	snap := l.Snapshot()
	if len(snap.Waiters) != 3 || snap.Waiters[0].RequestID != "older-child" ||
		snap.Waiters[1].RequestID != "older-child-2" || snap.Waiters[2].RequestID != "newer-child" {
		t.Fatalf("owner-ranked waiters = %+v", snap.Waiters)
	}
	if snap.Waiters[0].OwnerID != "owner-older" || snap.Waiters[0].OwnerAdmit != snap.Leases[0].Admit {
		t.Fatalf("older owner identity/rank not persisted: waiter=%+v leases=%+v", snap.Waiters[0], snap.Leases)
	}

	restored, err := Restore(snap, func() string { return "restored" })
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	events := mustRelease(t, restored, holder.ID, "holder")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "older-child" {
		t.Fatalf("first promoted after restore = %q, want older-child", events[1].RequestID)
	}

	_ = older
	_ = newer
}

func TestOwnerAdmissionRankRespectsPriorityAndOwnerlessFIFO(t *testing.T) {
	l := testLedger(t, 1, 0)
	mustGrant(t, l, Request{ID: "owner-older"})
	mustGrant(t, l, Request{ID: "owner-newer"})
	holder := mustGrant(t, l, Request{ID: "holder", Cores: 1})
	mustQueue(t, l, Request{ID: "ownerless-first", Cores: 1})
	mustQueue(t, l, Request{ID: "newer-high", OwnerID: "owner-newer", Cores: 1, Priority: 10})
	mustQueue(t, l, Request{ID: "older-low", OwnerID: "owner-older", Cores: 1})
	mustQueue(t, l, Request{ID: "ownerless-second", Cores: 1})

	events := mustRelease(t, l, holder.ID, "holder")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "newer-high" {
		t.Fatalf("first promoted = %q, want higher-priority newer-high", events[1].RequestID)
	}
	snap := l.Snapshot()
	got := []string{snap.Waiters[0].RequestID, snap.Waiters[1].RequestID, snap.Waiters[2].RequestID}
	want := []string{"older-low", "ownerless-first", "ownerless-second"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("remaining order = %v, want %v", got, want)
		}
	}
}

func TestCancelWaiterPreservesProtectedHeadAdmission(t *testing.T) {
	l := testLedger(t, 0, 0)
	older := mustGrant(t, l, Request{ID: "older", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	mustGrant(t, l, Request{ID: "other-holder", Semaphores: []SemaphoreClaim{sem("j", 1, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "heavy", Semaphores: []SemaphoreClaim{sem("k", 10, 8, PolicyQueue)}})
	light1 := mustGrant(t, l, Request{ID: "light-1", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "cancel-me", Semaphores: []SemaphoreClaim{sem("j", 1, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "light-2", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})

	events := l.CancelWaiter("cancel-me")
	if len(events) != 0 {
		t.Fatalf("cancel-me events = %+v, want no promotion", events)
	}
	events = mustRelease(t, l, older.ID, "older")
	wantKinds(t, events, EventReleased)
	snap := l.Snapshot()
	if len(snap.Waiters) != 2 || snap.Waiters[0].RequestID != "heavy" || snap.Waiters[1].RequestID != "light-2" {
		t.Fatalf("waiters after older release = %+v, want heavy still protecting light-2", snap.Waiters)
	}

	events = mustRelease(t, l, light1.ID, "light-1")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "heavy" {
		t.Fatalf("promoted %q, want protected heavy head", events[1].RequestID)
	}
}
