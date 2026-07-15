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
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "light" {
		t.Fatalf("promoted %q, want light backfilling behind the non-fitting heavy head", events[1].RequestID)
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
