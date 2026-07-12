package admission

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func testLedger(t *testing.T, cores float64, memory uint64) *Ledger {
	t.Helper()
	l, err := New(Config{TotalCores: cores, TotalMemoryBytes: memory, TokenGen: sequentialTokens()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func sequentialTokens() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("token-%d", n)
	}
}

func sem(key string, capacity, cost int, policy Policy) SemaphoreClaim {
	return SemaphoreClaim{Key: key, Capacity: capacity, Cost: cost, Policy: policy}
}

func submit(t *testing.T, l *Ledger, req Request) (Decision, []Event) {
	t.Helper()
	d, events, err := l.Submit(req)
	if err != nil {
		t.Fatalf("Submit(%q): %v", req.ID, err)
	}
	return d, events
}

func mustGrant(t *testing.T, l *Ledger, req Request) Lease {
	t.Helper()
	d, _ := submit(t, l, req)
	if d.Kind != DecisionGranted {
		t.Fatalf("Submit(%q) = %s, want %s", req.ID, d.Kind, DecisionGranted)
	}
	return d.Lease
}

func mustQueue(t *testing.T, l *Ledger, req Request) int {
	t.Helper()
	d, _ := submit(t, l, req)
	if d.Kind != DecisionQueued {
		t.Fatalf("Submit(%q) = %s, want %s", req.ID, d.Kind, DecisionQueued)
	}
	return d.Position
}

func mustRelease(t *testing.T, l *Ledger, id LeaseID, member string) []Event {
	t.Helper()
	events, err := l.Release(id, member)
	if err != nil {
		t.Fatalf("Release(%s, %q): %v", id, member, err)
	}
	return events
}

func eventKinds(events []Event) []EventKind {
	kinds := make([]EventKind, len(events))
	for i, ev := range events {
		kinds[i] = ev.Kind
	}
	return kinds
}

func wantKinds(t *testing.T, events []Event, want ...EventKind) {
	t.Helper()
	got := eventKinds(events)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
}

func TestSubmit_RejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name    string
		req     Request
		wantErr error
	}{
		{"empty id", Request{Cores: 1}, ErrInvalidRequest},
		{"negative cores", Request{ID: "r", Cores: -1}, ErrInvalidRequest},
		{"nan cores", Request{ID: "r", Cores: math.NaN()}, ErrInvalidRequest},
		{"inf cores", Request{ID: "r", Cores: math.Inf(1)}, ErrInvalidRequest},
		{"empty semaphore key", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("", 1, 1, PolicyQueue)}}, ErrInvalidRequest},
		{"duplicate semaphore key", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue), sem("k", 2, 1, PolicyQueue)}}, ErrInvalidRequest},
		{"zero capacity", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("k", 0, 0, PolicyQueue)}}, ErrInvalidRequest},
		{"negative cost", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("k", 2, -1, PolicyQueue)}}, ErrInvalidRequest},
		{"unknown policy", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("k", 2, 1, Policy("coalesce"))}}, ErrInvalidRequest},
		{"semaphore cost above capacity", Request{ID: "r", Semaphores: []SemaphoreClaim{sem("k", 3, 5, PolicyQueue)}}, ErrNeverAdmissible},
		{"cores above total", Request{ID: "r", Cores: 10}, ErrNeverAdmissible},
		{"memory above total", Request{ID: "r", MemoryBytes: 2048}, ErrNeverAdmissible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := testLedger(t, 4, 1024)
			before := l.Snapshot()
			d, events, err := l.Submit(tc.req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Submit = (%+v, %v), want error %v", d, err, tc.wantErr)
			}
			if len(events) != 0 {
				t.Fatalf("rejected submit emitted events %v", events)
			}
			if !reflect.DeepEqual(before, l.Snapshot()) {
				t.Fatal("rejected submit mutated the ledger")
			}
		})
	}
}

func TestNew_RejectsInvalidConfig(t *testing.T) {
	for _, cores := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := New(Config{TotalCores: cores}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("New(cores=%v) error = %v, want %v", cores, err, ErrInvalidConfig)
		}
	}
}

func TestSubmit_GrantsWhenEverythingFits(t *testing.T) {
	l := testLedger(t, 4, 1024)
	d, events, err := l.Submit(Request{
		ID:          "run-1",
		Cores:       2.5,
		MemoryBytes: 512,
		Semaphores:  []SemaphoreClaim{sem("deploy", 2, 1, PolicyQueue)},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if d.Kind != DecisionGranted {
		t.Fatalf("kind = %s, want granted", d.Kind)
	}
	if d.Lease.ID == "" || d.Lease.Token == "" {
		t.Fatalf("granted lease incomplete: %+v", d.Lease)
	}
	wantKinds(t, events, EventGranted)
	if events[0].RequestID != "run-1" || events[0].Lease != d.Lease.ID {
		t.Fatalf("granted event = %+v", events[0])
	}
	snap := l.Snapshot()
	if len(snap.Leases) != 1 || snap.Leases[0].MilliCores != 2500 || snap.Leases[0].MemoryBytes != 512 {
		t.Fatalf("lease state = %+v", snap.Leases)
	}
	if len(snap.Semaphores) != 1 || len(snap.Semaphores[0].Holds) != 1 {
		t.Fatalf("semaphore state = %+v", snap.Semaphores)
	}
}

func TestSubmit_ZeroResourceRequestIsGranted(t *testing.T) {
	l := testLedger(t, 0, 0)
	lease := mustGrant(t, l, Request{ID: "anchor"})
	if lease.ID == "" {
		t.Fatal("zero-resource grant has no lease")
	}
}

func TestSubmit_RejectsDuplicateParticipantIDs(t *testing.T) {
	l := testLedger(t, 2, 0)
	mustGrant(t, l, Request{ID: "holder", Cores: 1})
	if _, _, err := l.Submit(Request{ID: "holder", Cores: 1}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("resubmit of holder: %v, want %v", err, ErrDuplicateID)
	}
	mustGrant(t, l, Request{ID: "filler", Cores: 1})
	mustQueue(t, l, Request{ID: "waiter", Cores: 1})
	if _, _, err := l.Submit(Request{ID: "waiter", Cores: 1}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("resubmit of waiter: %v, want %v", err, ErrDuplicateID)
	}
}

func TestQueuePolicy_QueuesAtCapacityAndPromotesInFIFOOrder(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})

	dB, eventsB := submit(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	if dB.Kind != DecisionQueued || dB.Position != 0 {
		t.Fatalf("b = %+v, want queued at position 0", dB)
	}
	wantKinds(t, eventsB, EventQueued)
	if eventsB[0].Position != 0 {
		t.Fatalf("b queued event position = %d, want 0", eventsB[0].Position)
	}

	dC, eventsC := submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	if dC.Kind != DecisionQueued || dC.Position != 1 {
		t.Fatalf("c = %+v, want queued at position 1", dC)
	}
	wantKinds(t, eventsC, EventQueued)

	events := mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "b" {
		t.Fatalf("promoted %q first, want b", events[1].RequestID)
	}
	leaseB, ok := l.LeaseByID(events[1].Lease)
	if !ok {
		t.Fatalf("promoted lease %s not live", events[1].Lease)
	}

	events = mustRelease(t, l, leaseB.ID, "b")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "c" {
		t.Fatalf("promoted %q, want c", events[1].RequestID)
	}
}

func TestFailPolicy_FailsWhenSemaphoreFull(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	before := l.Snapshot()
	d, events := submit(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyFail)}})
	if d.Kind != DecisionFailed || d.Key != "k" {
		t.Fatalf("decision = %+v, want failed on k", d)
	}
	if len(events) != 0 {
		t.Fatalf("failed decision emitted events %v", events)
	}
	if !reflect.DeepEqual(before, l.Snapshot()) {
		t.Fatal("failed decision mutated the ledger")
	}
}

func TestSkipPolicy_SkipsWhenSemaphoreFull(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	d, events := submit(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicySkip)}})
	if d.Kind != DecisionSkipped || d.Key != "k" {
		t.Fatalf("decision = %+v, want skipped on k", d)
	}
	if len(events) != 0 {
		t.Fatalf("skipped decision emitted events %v", events)
	}
}

func TestFailPolicy_QueuesWhenOnlyHostBlocks(t *testing.T) {
	l := testLedger(t, 2, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Cores: 2})
	pos := mustQueue(t, l, Request{ID: "b", Cores: 2, Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyFail)}})
	if pos != 0 {
		t.Fatalf("position = %d, want 0", pos)
	}
	events := mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "b" {
		t.Fatalf("promoted %q, want b", events[1].RequestID)
	}
}

func TestFailPolicy_BackfillsPastWaiterBlockedByOlderHolder(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 2, 2, PolicyQueue)}})
	d, events := submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyFail)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("decision = %+v, want granted: budget fits and the heavy head waits only on an older holder", d)
	}
	wantKinds(t, events, EventGranted)
}

func TestFailPolicy_FailsBehindProtectedWaiter(t *testing.T) {
	l := testLedger(t, 0, 0)
	older := mustGrant(t, l, Request{ID: "older", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "heavy", Semaphores: []SemaphoreClaim{sem("k", 10, 8, PolicyQueue)}})
	mustGrant(t, l, Request{ID: "younger", Semaphores: []SemaphoreClaim{sem("k", 10, 5, PolicyQueue)}})
	events := mustRelease(t, l, older.ID, "older")
	wantKinds(t, events, EventReleased)
	d, _ := submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 10, 3, PolicyFail)}})
	if d.Kind != DecisionFailed || d.Key != "k" {
		t.Fatalf("decision = %+v, want failed: the heavy head is protected by a younger holder", d)
	}
}

func TestCancelOthers_EvictsOldestFirstUntilFit(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 3, 1, PolicyQueue)}})
	leaseB := mustGrant(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 3, 1, PolicyQueue)}})
	leaseC := mustGrant(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 3, 1, PolicyQueue)}})

	d, events := submit(t, l, Request{ID: "d", Semaphores: []SemaphoreClaim{sem("k", 3, 2, PolicyCancelOthers)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("decision = %+v, want granted", d)
	}
	if !reflect.DeepEqual(d.Evicted, []LeaseID{leaseA.ID, leaseB.ID}) {
		t.Fatalf("evicted = %v, want oldest-first [%s %s]", d.Evicted, leaseA.ID, leaseB.ID)
	}
	wantKinds(t, events, EventEvicted, EventEvicted, EventGranted)
	for i, victim := range []struct {
		requestID string
		lease     LeaseID
	}{{"a", leaseA.ID}, {"b", leaseB.ID}} {
		ev := events[i]
		if ev.RequestID != victim.requestID || ev.Lease != victim.lease || ev.Key != "k" || ev.SupersededBy != d.Lease.ID {
			t.Fatalf("eviction event %d = %+v", i, ev)
		}
	}

	snap := l.Snapshot()
	if len(snap.Semaphores) != 1 {
		t.Fatalf("semaphores = %+v", snap.Semaphores)
	}
	superseded := map[LeaseID]bool{}
	for _, h := range snap.Semaphores[0].Holds {
		superseded[h.Lease] = h.Superseded
	}
	want := map[LeaseID]bool{leaseA.ID: true, leaseB.ID: true, leaseC.ID: false, d.Lease.ID: false}
	if !reflect.DeepEqual(superseded, want) {
		t.Fatalf("superseded map = %v, want %v", superseded, want)
	}

	mustQueue(t, l, Request{ID: "e", Semaphores: []SemaphoreClaim{sem("k", 3, 1, PolicyQueue)}})
	events = mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased)
	events = mustRelease(t, l, leaseC.ID, "c")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "e" {
		t.Fatalf("promoted %q, want e", events[1].RequestID)
	}
}

func TestCancelOthers_GrantsWithoutEvictionWhenFits(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})
	d, events := submit(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyCancelOthers)}})
	if d.Kind != DecisionGranted || len(d.Evicted) != 0 {
		t.Fatalf("decision = %+v, want grant with no evictions", d)
	}
	wantKinds(t, events, EventGranted)
}

func TestCancelOthers_JumpsQueueAheadOfWaiters(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})

	d, events := submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyCancelOthers)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("decision = %+v, want granted past the queue", d)
	}
	if !reflect.DeepEqual(d.Evicted, []LeaseID{leaseA.ID}) {
		t.Fatalf("evicted = %v, want [%s]", d.Evicted, leaseA.ID)
	}
	wantKinds(t, events, EventEvicted, EventGranted)

	if got := len(l.Snapshot().Waiters); got != 1 {
		t.Fatalf("waiters = %d, want b still queued", got)
	}
	events = mustRelease(t, l, d.Lease.ID, "c")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "b" {
		t.Fatalf("promoted %q, want b", events[1].RequestID)
	}
}

func TestCancelOthers_DefersEvictionUntilFullGrant(t *testing.T) {
	l := testLedger(t, 2, 0)
	leaseX := mustGrant(t, l, Request{ID: "x", Cores: 2})
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})

	pos := mustQueue(t, l, Request{ID: "r", Cores: 2, Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyCancelOthers)}})
	if pos != 0 {
		t.Fatalf("position = %d, want 0", pos)
	}
	for _, h := range l.Snapshot().Semaphores[0].Holds {
		if h.Superseded {
			t.Fatal("victim superseded while the superseder is still queued on host cores")
		}
	}

	events := mustRelease(t, l, leaseX.ID, "x")
	wantKinds(t, events, EventReleased, EventEvicted, EventPromoted)
	if events[1].Lease != leaseA.ID || events[1].SupersededBy != events[2].Lease {
		t.Fatalf("eviction event = %+v, promotion event = %+v", events[1], events[2])
	}
	if events[2].RequestID != "r" {
		t.Fatalf("promoted %q, want r", events[2].RequestID)
	}
}

func TestAllOrNothing_WaiterHoldsNothingWhileQueued(t *testing.T) {
	l := testLedger(t, 1, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Cores: 1})
	mustQueue(t, l, Request{ID: "b", Cores: 1, Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})

	snap := l.Snapshot()
	if len(snap.Semaphores) != 0 {
		t.Fatalf("queued request holds semaphores: %+v", snap.Semaphores)
	}
	if len(snap.Leases) != 1 {
		t.Fatalf("leases = %+v, want only a", snap.Leases)
	}

	events := mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted)
	snap = l.Snapshot()
	if len(snap.Semaphores) != 1 || len(snap.Leases) != 1 {
		t.Fatalf("promotion did not take every resource at once: %+v", snap)
	}
}

func TestWeighted_LightArrivalBackfillsPastHeavyHead(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 3, 2, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "heavy", Semaphores: []SemaphoreClaim{sem("k", 3, 3, PolicyQueue)}})

	d, events := submit(t, l, Request{ID: "light", Semaphores: []SemaphoreClaim{sem("k", 3, 1, PolicyQueue)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("light = %+v, want granted as backfill past the heavy head", d)
	}
	wantKinds(t, events, EventGranted)
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "heavy" {
		t.Fatalf("waiters = %+v, want only the heavy head still queued", snap.Waiters)
	}

	events = mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased)
	events = mustRelease(t, l, d.Lease.ID, "light")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "heavy" {
		t.Fatalf("promoted %q, want the heavy head once budget frees", events[1].RequestID)
	}
}

func TestFIFO_DisjointResourcesBypassBlockedQueue(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	mustGrant(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("j", 1, 1, PolicyQueue)}})
}

func TestWeighted_ZeroCostClaimBackfillsPastBlockedWaiter(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	d, events := submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 1, 0, PolicyQueue)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("zero-cost c = %+v, want granted: it draws no budget and never delays b", d)
	}
	wantKinds(t, events, EventGranted)
	if snap := l.Snapshot(); len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "b" {
		t.Fatalf("waiters = %+v, want only b queued", snap.Waiters)
	}
}

func TestFIFO_ZeroCostClaimGrantsAtCapacityWithEmptyQueue(t *testing.T) {
	l := testLedger(t, 0, 0)
	mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	mustGrant(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 1, 0, PolicyQueue)}})
}

func TestWeighted_CapacitySkewMostRestrictiveWins(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 5, 3, PolicyQueue)}})

	events := mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "b" {
		t.Fatalf("promoted %q, want b once the tighter declaration drained", events[1].RequestID)
	}
}

func TestWeighted_TiesPromoteInArrivalOrder(t *testing.T) {
	l := testLedger(t, 0, 0)
	leaseA := mustGrant(t, l, Request{ID: "a", Semaphores: []SemaphoreClaim{sem("k", 2, 2, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "b", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})
	mustQueue(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})

	events := mustRelease(t, l, leaseA.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted, EventPromoted)
	if events[1].RequestID != "b" || events[2].RequestID != "c" {
		t.Fatalf("promotion order = %q, %q; want b then c", events[1].RequestID, events[2].RequestID)
	}
}

func TestAttach_MemberKeepsLeaseAliveAfterOwnerReleases(t *testing.T) {
	l := testLedger(t, 2, 0)
	lease := mustGrant(t, l, Request{ID: "parent", Cores: 2})
	if err := l.Attach(lease.ID, "child"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	mustQueue(t, l, Request{ID: "other", Cores: 2})

	events := mustRelease(t, l, lease.ID, "parent")
	if len(events) != 0 {
		t.Fatalf("owner release emitted %v while a member is attached", events)
	}
	if _, ok := l.LeaseByID(lease.ID); !ok {
		t.Fatal("lease died while a member was attached")
	}

	events = mustRelease(t, l, lease.ID, "child")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "other" {
		t.Fatalf("promoted %q, want other", events[1].RequestID)
	}
}

func TestAttach_DrawsNoBudget(t *testing.T) {
	l := testLedger(t, 1, 0)
	lease := mustGrant(t, l, Request{ID: "parent", Cores: 1, Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	before := l.Snapshot()
	if err := l.Attach(lease.ID, "child"); err != nil {
		t.Fatalf("Attach at full capacity: %v", err)
	}
	after := l.Snapshot()
	before.Leases[0].Members = nil
	after.Leases[0].Members = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("attach changed ledger state beyond membership")
	}
}

func TestAttach_Errors(t *testing.T) {
	l := testLedger(t, 2, 0)
	lease := mustGrant(t, l, Request{ID: "parent", Cores: 1})
	mustGrant(t, l, Request{ID: "filler", Cores: 1})
	mustQueue(t, l, Request{ID: "waiting", Cores: 1})

	if err := l.Attach("lease-999", "child"); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("unknown lease: %v", err)
	}
	if err := l.Attach(lease.ID, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty member: %v", err)
	}
	if err := l.Attach(lease.ID, "parent"); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("owner re-attach: %v", err)
	}
	if err := l.Attach(lease.ID, "waiting"); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("waiter attach: %v", err)
	}
}

func TestRelease_Errors(t *testing.T) {
	l := testLedger(t, 2, 0)
	lease := mustGrant(t, l, Request{ID: "a", Cores: 1})

	if _, err := l.Release("lease-999", "a"); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("unknown lease: %v", err)
	}
	if _, err := l.Release(lease.ID, "nobody"); !errors.Is(err, ErrUnknownMember) {
		t.Fatalf("unknown member: %v", err)
	}
	mustRelease(t, l, lease.ID, "a")
	if _, err := l.Release(lease.ID, "a"); !errors.Is(err, ErrUnknownLease) {
		t.Fatalf("double release: %v", err)
	}
}

func TestReattach_ResolvesLiveTokensOnly(t *testing.T) {
	l := testLedger(t, 2, 0)
	lease := mustGrant(t, l, Request{ID: "a", Cores: 1})

	id, err := l.Reattach(lease.Token)
	if err != nil || id != lease.ID {
		t.Fatalf("Reattach = (%s, %v), want (%s, nil)", id, err, lease.ID)
	}
	if _, err := l.Reattach("bogus"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("bogus token: %v", err)
	}
	mustRelease(t, l, lease.ID, "a")
	if _, err := l.Reattach(lease.Token); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("released token: %v", err)
	}
}

func TestSetHeadroom_ShrinkGatesNewAdmissionWithoutEvicting(t *testing.T) {
	l := testLedger(t, 4, 0)
	mustGrant(t, l, Request{ID: "a", Cores: 2})

	events, err := l.SetHeadroom(2, 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("SetHeadroom = (%v, %v)", events, err)
	}
	if got := len(l.Snapshot().Leases); got != 1 {
		t.Fatalf("headroom shrink evicted: %d leases", got)
	}

	mustQueue(t, l, Request{ID: "b", Cores: 2})

	events, err = l.SetHeadroom(4, 0)
	if err != nil {
		t.Fatalf("SetHeadroom: %v", err)
	}
	wantKinds(t, events, EventPromoted)
	if events[0].RequestID != "b" {
		t.Fatalf("promoted %q, want b", events[0].RequestID)
	}
}

func TestSetHeadroom_ShrinkBelowGrantedNeverEvicts(t *testing.T) {
	l := testLedger(t, 4, 0)
	lease := mustGrant(t, l, Request{ID: "a", Cores: 4})
	if _, err := l.SetHeadroom(1, 0); err != nil {
		t.Fatalf("SetHeadroom: %v", err)
	}
	mustQueue(t, l, Request{ID: "b", Cores: 1})

	events := mustRelease(t, l, lease.ID, "a")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "b" {
		t.Fatalf("promoted %q, want b within the shrunken headroom", events[1].RequestID)
	}
	mustQueue(t, l, Request{ID: "c", Cores: 1})
}

func TestSetHeadroom_RejectsInvalidValues(t *testing.T) {
	l := testLedger(t, 4, 0)
	for _, cores := range []float64{-1, math.NaN(), math.Inf(1)} {
		if _, err := l.SetHeadroom(cores, 0); !errors.Is(err, ErrInvalidHeadroom) {
			t.Fatalf("SetHeadroom(%v) error = %v, want %v", cores, err, ErrInvalidHeadroom)
		}
	}
}

func TestLeaseByID_ReportsLiveness(t *testing.T) {
	l := testLedger(t, 2, 0)
	lease := mustGrant(t, l, Request{ID: "a", Cores: 1})
	got, ok := l.LeaseByID(lease.ID)
	if !ok || got != lease {
		t.Fatalf("LeaseByID = (%+v, %v), want (%+v, true)", got, ok, lease)
	}
	mustRelease(t, l, lease.ID, "a")
	if _, ok := l.LeaseByID(lease.ID); ok {
		t.Fatal("released lease still resolves")
	}
}

func TestNew_DefaultTokenGeneratorMintsUniqueTokens(t *testing.T) {
	l, err := New(Config{TotalCores: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	first := mustGrant(t, l, Request{ID: "a"})
	second := mustGrant(t, l, Request{ID: "b"})
	if len(first.Token) != 32 || len(second.Token) != 32 {
		t.Fatalf("token lengths = %d, %d; want 32 hex chars", len(first.Token), len(second.Token))
	}
	if first.Token == second.Token {
		t.Fatal("default generator repeated a token")
	}
}

func TestGrant_RetriesTokensTheGeneratorRepeats(t *testing.T) {
	tokens := []string{"dup", "dup", "fresh"}
	l, err := New(Config{TotalCores: 1, TokenGen: func() string {
		tok := tokens[0]
		if len(tokens) > 1 {
			tokens = tokens[1:]
		}
		return tok
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustGrant(t, l, Request{ID: "a"})
	lease := mustGrant(t, l, Request{ID: "b"})
	if lease.Token != "fresh" {
		t.Fatalf("token = %q, want the retried %q", lease.Token, "fresh")
	}
}

func TestInvariants_ViolationPanicsFailFast(t *testing.T) {
	l := testLedger(t, 4, 0)
	mustGrant(t, l, Request{ID: "a", Cores: 1})
	l.usedMilliCores = 999999
	defer func() {
		if recover() == nil {
			t.Fatal("corrupted ledger did not panic")
		}
	}()
	l.mustHoldInvariants()
}

func TestEvents_SequenceIsMonotonicAndContiguous(t *testing.T) {
	l := testLedger(t, 2, 0)
	var all []Event
	collect := func(events []Event) {
		all = append(all, events...)
	}

	d, events := submit(t, l, Request{ID: "a", Cores: 2, Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
	collect(events)
	_, events = submit(t, l, Request{ID: "b", Cores: 1})
	collect(events)
	_, events = submit(t, l, Request{ID: "c", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyCancelOthers)}})
	collect(events)
	collect(mustRelease(t, l, d.Lease.ID, "a"))

	for i, ev := range all {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d, want %d: %+v", i, ev.Seq, i+1, ev)
		}
	}
}
