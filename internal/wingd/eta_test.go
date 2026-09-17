package wingd

import (
	"math"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestSimulateQueue_ETA(t *testing.T) {
	inf := math.Inf(1)
	cases := []struct {
		name       string
		capCores   float64
		holders    []simRun
		waiters    []simRun
		wantStarts []float64
		wantClear  float64
	}{
		{
			name:       "both fit immediately",
			capCores:   8,
			waiters:    []simRun{{cores: 4, duration: 10000}, {cores: 4, duration: 10000}},
			wantStarts: []float64{0, 0},
			wantClear:  10000,
		},
		{
			name:       "second serializes behind first",
			capCores:   8,
			waiters:    []simRun{{cores: 8, duration: 5000}, {cores: 8, duration: 3000}},
			wantStarts: []float64{0, 5000},
			wantClear:  8000,
		},
		{
			name:       "waiter waits for a holder to free capacity",
			capCores:   8,
			holders:    []simRun{{cores: 8, finish: 4000}},
			waiters:    []simRun{{cores: 8, duration: 2000}},
			wantStarts: []float64{4000},
			wantClear:  6000,
		},
		{
			name:       "unknown holder duration blocks ETA",
			capCores:   8,
			holders:    []simRun{{cores: 8, finish: inf}},
			waiters:    []simRun{{cores: 8, duration: 2000}},
			wantStarts: []float64{inf},
			wantClear:  inf,
		},
		{
			name:       "waiter that fits gets a start even with unknown duration",
			capCores:   8,
			waiters:    []simRun{{cores: 4, duration: inf}},
			wantStarts: []float64{0},
			wantClear:  inf,
		},
		{
			name:       "waiter above current headroom keeps clear unknown",
			capCores:   1,
			waiters:    []simRun{{cores: 2, duration: 10000}},
			wantStarts: []float64{inf},
			wantClear:  inf,
		},
		{
			name:       "waiter behind unstartable head also stays unknown",
			capCores:   1,
			waiters:    []simRun{{cores: 2, duration: 10000}, {cores: 1, duration: 10000}},
			wantStarts: []float64{inf, inf},
			wantClear:  inf,
		},
		{
			name:       "oversized soft CPU starts after holders drain",
			capCores:   1,
			holders:    []simRun{{cores: 1, finish: 4000}},
			waiters:    []simRun{{cores: 2, softCores: true, duration: 1000}},
			wantStarts: []float64{4000},
			wantClear:  5000,
		},
		{
			name:       "zero core waiter fits during soft CPU overcommit",
			capCores:   1,
			waiters:    []simRun{{cores: 2, softCores: true, duration: 5000}, {mem: 1, duration: 1000}},
			wantStarts: []float64{0, 0},
			wantClear:  5000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			starts, clear := simulateQueue(tc.capCores, 1<<50, tc.holders, tc.waiters)
			if len(starts) != len(tc.wantStarts) {
				t.Fatalf("starts len = %d, want %d", len(starts), len(tc.wantStarts))
			}
			for i := range starts {
				if !approxEqInf(starts[i], tc.wantStarts[i]) {
					t.Errorf("start[%d] = %v, want %v", i, starts[i], tc.wantStarts[i])
				}
			}
			if !approxEqInf(clear, tc.wantClear) {
				t.Errorf("clear = %v, want %v", clear, tc.wantClear)
			}
		})
	}
}

func TestRemainingMS_OverdueHolderDoesNotPromiseImmediateRelease(t *testing.T) {
	for _, elapsed := range []int64{10_000, 10_001} {
		got := remainingMS(10_000, elapsed)
		if !math.IsInf(got, 1) {
			t.Errorf("remainingMS(10000, %d) = %v, want +Inf: a holder at or past its estimate is still active", elapsed, got)
		}
	}
}

func TestETAOverflowIsUnknown(t *testing.T) {
	huge := int64(^uint64(0) >> 1)
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{{RunID: "holder", Resources: wingwire.HostResources{Cores: 1}, ExpectedDurationMS: huge}},
		Waiters: []wingwire.Waiter{{RunID: "waiter", Resources: wingwire.HostResources{Cores: 1}, ExpectedDurationMS: huge}},
	}
	snap := admission.Snapshot{
		TotalMilliCores:    1000,
		HeadroomMilliCores: 1000,
		Leases:             []admission.LeaseState{{ID: "lease", RequestID: "holder", MilliCores: 1000}},
		Waiters:            []admission.WaiterState{{RequestID: "waiter", MilliCores: 1000}},
	}
	annotateETA(&qs, snap)
	assertETA(t, "ExpectedStartMS", qs.Waiters[0].ExpectedStartMS, semaNone)
	assertETA(t, "ExpectedClearMS", qs.ExpectedClearMS, semaNone)
}

func approxEqInf(a, b float64) bool {
	if math.IsInf(a, 1) || math.IsInf(b, 1) {
		return math.IsInf(a, 1) && math.IsInf(b, 1)
	}
	return math.Abs(a-b) < 1e-6
}

func TestQueueBlockingReason_FillsArrivalOrderWait(t *testing.T) {
	if got := queueBlockingReason("", nil, 2); got != "waiting behind earlier queued work" {
		t.Fatalf("queueBlockingReason = %q, want arrival-order reason", got)
	}
	if got := queueBlockingReason("needs 4.0 cores; 1.0 available", nil, 2); got != "needs 4.0 cores; 1.0 available" {
		t.Fatalf("queueBlockingReason replaced host reason with %q", got)
	}
	if got := queueBlockingReason("", []string{"deploy"}, 2); got != `waiting for semaphore "deploy"` {
		t.Fatalf("queueBlockingReason = %q, want named semaphore cause", got)
	}
	if got := queueBlockingReason("", nil, 1); got != "" {
		t.Fatalf("queueBlockingReason = %q, want first waiter to remain unexplained by arrival order", got)
	}
}

func TestSimulateAdmissionETA_AgreesWithTheLedgerUnderAHeadroomSqueeze(t *testing.T) {
	ledger, err := admission.New(admission.Config{TotalMemoryBytes: 8 << 30})
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	holder := grantMemory(t, ledger, "holder", 5<<30)
	queueMemory(t, ledger, "heavy", 6<<30)
	grantMemory(t, ledger, "backfill", 1<<30)
	if _, err := ledger.SetHeadroom(0, 4<<30); err != nil {
		t.Fatalf("squeeze headroom: %v", err)
	}
	if _, err := ledger.Release(holder, "holder"); err != nil {
		t.Fatalf("release holder: %v", err)
	}

	snap := ledger.Snapshot()
	if len(snap.Waiters) != 1 || snap.Waiters[0].BackfillCount != 1 {
		t.Fatalf("waiters = %+v, want heavy alone and protected after one backfill", snap.Waiters)
	}
	snap.Waiters = append(snap.Waiters, admission.WaiterState{
		RequestID:   "small",
		Admit:       snap.AdmitSeq + 1,
		MemoryBytes: 64 << 20,
	})
	var qs wingwire.QueueState
	for _, lease := range snap.Leases {
		qs.Holders = append(qs.Holders, wingwire.Holder{RunID: lease.RequestID, ExpectedDurationMS: 10_000})
	}
	for _, waiter := range snap.Waiters {
		qs.Waiters = append(qs.Waiters, wingwire.Waiter{RunID: waiter.RequestID, ExpectedDurationMS: 1_000})
	}

	starts, _ := simulateAdmissionETA(&qs, snap)
	decision, _, err := ledger.Submit(admission.Request{ID: "small", MemoryBytes: 64 << 20})
	if err != nil {
		t.Fatalf("submit small: %v", err)
	}
	if decision.Kind != admission.DecisionGranted {
		t.Fatalf("ledger answered small with %s, want %s", decision.Kind, admission.DecisionGranted)
	}
	if starts[1] != 0 {
		t.Fatalf("estimated start for small = %v, want 0: the ledger admits it now, so an estimate "+
			"promising a wait describes a queue this daemon does not have", starts[1])
	}
}

func grantMemory(t *testing.T, ledger *admission.Ledger, id string, bytes uint64) admission.LeaseID {
	t.Helper()
	decision, _, err := ledger.Submit(admission.Request{ID: id, MemoryBytes: bytes})
	if err != nil {
		t.Fatalf("submit %q: %v", id, err)
	}
	if decision.Kind != admission.DecisionGranted {
		t.Fatalf("submit %q = %s, want %s", id, decision.Kind, admission.DecisionGranted)
	}
	return decision.Lease.ID
}

func queueMemory(t *testing.T, ledger *admission.Ledger, id string, bytes uint64) {
	t.Helper()
	decision, _, err := ledger.Submit(admission.Request{ID: id, MemoryBytes: bytes})
	if err != nil {
		t.Fatalf("submit %q: %v", id, err)
	}
	if decision.Kind != admission.DecisionQueued {
		t.Fatalf("submit %q = %s, want %s", id, decision.Kind, admission.DecisionQueued)
	}
}
