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

func TestWeighted_HostCoresBackfillPastHeavyHead(t *testing.T) {
	l := testLedger(t, 8, 0)
	holder := mustGrant(t, l, Request{ID: "holder", Cores: 6})
	mustQueue(t, l, Request{ID: "heavy", Cores: 6})

	d, _ := submit(t, l, Request{ID: "light", Cores: 2})
	if d.Kind != DecisionGranted {
		t.Fatalf("light = %+v, want granted: 2 free cores backfill past the heavy head blocked by the older holder", d)
	}
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "heavy" {
		t.Fatalf("waiters = %+v, want only heavy queued", snap.Waiters)
	}

	events := mustRelease(t, l, holder.ID, "holder")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "heavy" {
		t.Fatalf("promoted %q, want heavy once the older holder frees its cores", events[1].RequestID)
	}
}
