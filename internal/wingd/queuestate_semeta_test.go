package wingd

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestMixedResourceETAUsesTheLaterKnownBoundOrUnknown(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		host, sem, elapsed, want int64
	}{
		{"host finishes last", 9000, 4000, 0, 9000},
		{"semaphore finishes last", 4000, 9000, 0, 9000},
		{"unknown host duration", 0, 4000, 0, semaNone},
		{"overdue semaphore", 4000, 7000, 7001, semaNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ledger, err := admission.New(admission.Config{TotalCores: 1})
			if err != nil {
				t.Fatal(err)
			}
			claims := []admission.SemaphoreClaim{{Key: "deploy", Capacity: 1, Cost: 1}}
			etaSubmit(t, ledger, admission.Request{ID: "host", Cores: 1}, admission.DecisionGranted)
			etaSubmit(t, ledger, admission.Request{ID: "sem", Semaphores: claims}, admission.DecisionGranted)
			etaSubmit(t, ledger, admission.Request{ID: "waiter", Cores: 1, Semaphores: claims}, admission.DecisionQueued)
			qs := wingwire.QueueState{
				Holders: []wingwire.Holder{{RunID: "host", ExpectedDurationMS: tc.host}, {RunID: "sem", ExpectedDurationMS: tc.sem, ElapsedMS: tc.elapsed}},
				Waiters: []wingwire.Waiter{{RunID: "waiter", ExpectedDurationMS: 1000}},
			}
			snap := ledger.Snapshot()
			annotateSemaphoreETA(&qs, snap)
			assertETA(t, "start", qs.Waiters[0].ExpectedStartMS, tc.want)
			clear := tc.want
			if clear != semaNone {
				clear += 1000
			}
			assertETA(t, "clear", qs.ExpectedClearMS, clear)
			if got := ledger.Snapshot(); got.EventSeq != snap.EventSeq || len(got.Waiters) != 1 {
				t.Fatal("ETA mutated live admission")
			}
		})
	}
}

func TestSemaphoreETARespectsTheHoldersCapacity(t *testing.T) {
	ledger, err := admission.New(admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	etaSubmit(t, ledger, admission.Request{ID: "holder", Semaphores: []admission.SemaphoreClaim{{Key: "pool", Capacity: 1, Cost: 1}}}, admission.DecisionGranted)
	etaSubmit(t, ledger, admission.Request{ID: "waiter", Semaphores: []admission.SemaphoreClaim{{Key: "pool", Capacity: 2, Cost: 1}}}, admission.DecisionQueued)
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "holder", ExpectedDurationMS: 7000}}, Waiters: []wingwire.Waiter{{RunID: "waiter", ExpectedDurationMS: 1000}}}
	annotateSemaphoreETA(&qs, ledger.Snapshot())
	assertETA(t, "start", qs.Waiters[0].ExpectedStartMS, 7000)
	assertETA(t, "clear", qs.ExpectedClearMS, 8000)
}

func TestSemaphoreETAIgnoresSupersededHolds(t *testing.T) {
	ledger, err := admission.New(admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"superseded", "current"} {
		etaSubmit(t, ledger, admission.Request{ID: id, Semaphores: []admission.SemaphoreClaim{{Key: "deploy", Capacity: 1, Cost: 1, Policy: admission.PolicyCancelOthers}}}, admission.DecisionGranted)
	}
	etaSubmit(t, ledger, admission.Request{ID: "waiter", Semaphores: []admission.SemaphoreClaim{{Key: "deploy", Capacity: 1, Cost: 1}}}, admission.DecisionQueued)
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "superseded"}, {RunID: "current", ExpectedDurationMS: 7000}}, Waiters: []wingwire.Waiter{{RunID: "waiter", ExpectedDurationMS: 1000}}}
	annotateSemaphoreETA(&qs, ledger.Snapshot())
	assertETA(t, "start", qs.Waiters[0].ExpectedStartMS, 7000)
	assertETA(t, "clear", qs.ExpectedClearMS, 8000)
}

func TestJointETAUsesExactLargeResourceBudgets(t *testing.T) {
	const boundary = uint64(1 << 53)
	ledger, err := admission.New(admission.Config{TotalMemoryBytes: boundary + 2})
	if err != nil {
		t.Fatal(err)
	}
	etaSubmit(t, ledger, admission.Request{ID: "holder", MemoryBytes: boundary + 1}, admission.DecisionGranted)
	etaSubmit(t, ledger, admission.Request{ID: "waiter", MemoryBytes: 2}, admission.DecisionQueued)
	qs := wingwire.QueueState{Holders: []wingwire.Holder{{RunID: "holder", ExpectedDurationMS: 7000}}, Waiters: []wingwire.Waiter{{RunID: "waiter", ExpectedDurationMS: 1000}}}
	annotateSemaphoreETA(&qs, ledger.Snapshot())
	assertETA(t, "start", qs.Waiters[0].ExpectedStartMS, 7000)
}

func TestAdmissionETAInvalidSnapshotIsUnknown(t *testing.T) {
	starts, clear := simulateAdmissionETA(&wingwire.QueueState{Waiters: []wingwire.Waiter{{}}}, admission.Snapshot{Waiters: []admission.WaiterState{{RequestID: "missing-owner-rank"}}})
	if !math.IsInf(starts[0], 1) || !math.IsInf(clear, 1) {
		t.Fatalf("invalid snapshot produced ETA %v %v", starts, clear)
	}
}

func etaSubmit(t *testing.T, l *admission.Ledger, req admission.Request, want admission.DecisionKind) admission.LeaseID {
	t.Helper()
	decision, _, err := l.Submit(req)
	if err != nil || decision.Kind != want {
		t.Fatalf("submit %s = %+v, %v", req.ID, decision, err)
	}
	return decision.Lease.ID
}

const semaNone = int64(-1)

func assertETA(t *testing.T, field string, got *int64, want int64) {
	t.Helper()
	if want == semaNone {
		if got != nil {
			t.Fatalf("%s = %d, want nil", field, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want %d", field, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", field, *got, want)
	}
}
