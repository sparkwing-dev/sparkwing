package wingd

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestAnnotateSemaphoreETA(t *testing.T) {
	cases := []struct {
		name string
		qs   wingwire.QueueState
		snap admission.Snapshot
		want []int64
	}{
		{
			name: "waiter starts when the one holder is expected to finish",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", ExpectedDurationMS: 10000, ElapsedMS: 4000, Semaphores: []string{"deploy"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "run-b", ExpectedDurationMS: 5000, Semaphores: []string{"deploy"}},
				},
			},
			snap: admission.Snapshot{
				Leases:     []admission.LeaseState{semLease("L1", "run-a", "deploy", 1, 1)},
				Semaphores: []admission.SemaphoreState{semState("deploy", 1, semHold("L1", 1, 1))},
				Waiters:    []admission.WaiterState{semWaiter("run-b", "deploy", 1, 1)},
			},
			want: []int64{6000},
		},
		{
			name: "unmeasured holder leaves the estimate nil",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", Semaphores: []string{"deploy"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "run-b", ExpectedDurationMS: 5000, Semaphores: []string{"deploy"}},
				},
			},
			snap: admission.Snapshot{
				Leases:     []admission.LeaseState{semLease("L1", "run-a", "deploy", 1, 1)},
				Semaphores: []admission.SemaphoreState{semState("deploy", 1, semHold("L1", 1, 1))},
				Waiters:    []admission.WaiterState{semWaiter("run-b", "deploy", 1, 1)},
			},
			want: []int64{semaNone},
		},
		{
			name: "overdue active holder leaves the estimate nil",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", ExpectedDurationMS: 10_000, ElapsedMS: 10_001, Semaphores: []string{"deploy"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "run-b", ExpectedDurationMS: 5_000, Semaphores: []string{"deploy"}},
				},
			},
			snap: admission.Snapshot{
				Leases:     []admission.LeaseState{semLease("L1", "run-a", "deploy", 1, 1)},
				Semaphores: []admission.SemaphoreState{semState("deploy", 1, semHold("L1", 1, 1))},
				Waiters:    []admission.WaiterState{semWaiter("run-b", "deploy", 1, 1)},
			},
			want: []int64{semaNone},
		},
		{
			name: "cheap waiters start before an expensive one that does not fit",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", ExpectedDurationMS: 10000, Semaphores: []string{"pool"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "cheap-1", ExpectedDurationMS: 2000, Semaphores: []string{"pool"}},
					{RunID: "cheap-2", ExpectedDurationMS: 2000, Semaphores: []string{"pool"}},
					{RunID: "expensive", ExpectedDurationMS: 1000, Semaphores: []string{"pool"}},
				},
			},
			snap: admission.Snapshot{
				Leases:     []admission.LeaseState{semLease("L1", "run-a", "pool", 4, 2)},
				Semaphores: []admission.SemaphoreState{semState("pool", 4, semHold("L1", 4, 2))},
				Waiters: []admission.WaiterState{
					semWaiter("cheap-1", "pool", 4, 1),
					semWaiter("cheap-2", "pool", 4, 1),
					semWaiter("expensive", "pool", 4, 3),
				},
			},
			want: []int64{0, 0, 10000},
		},
		{
			name: "a waiter on two keys starts when the slower one lets it in",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", ExpectedDurationMS: 3000, Semaphores: []string{"fast"}},
					{RunID: "run-b", ExpectedDurationMS: 9000, Semaphores: []string{"slow"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "run-c", ExpectedDurationMS: 1000, Semaphores: []string{"fast", "slow"}},
				},
			},
			snap: admission.Snapshot{
				Leases: []admission.LeaseState{
					semLease("L1", "run-a", "fast", 1, 1),
					semLease("L2", "run-b", "slow", 1, 1),
				},
				Semaphores: []admission.SemaphoreState{
					semState("fast", 1, semHold("L1", 1, 1)),
					semState("slow", 1, semHold("L2", 1, 1)),
				},
				Waiters: []admission.WaiterState{{
					RequestID: "run-c",
					Claims: []admission.ClaimState{
						{Key: "fast", Capacity: 1, Cost: 1},
						{Key: "slow", Capacity: 1, Cost: 1},
					},
				}},
			},
			want: []int64{9000},
		},
		{
			name: "a superseded hold frees nothing and blocks nothing",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{
					{RunID: "run-a", Semaphores: []string{"deploy"}},
				},
				Waiters: []wingwire.Waiter{
					{RunID: "run-b", ExpectedDurationMS: 5000, Semaphores: []string{"deploy"}},
				},
			},
			snap: admission.Snapshot{
				Leases: []admission.LeaseState{semLease("L1", "run-a", "deploy", 1, 1)},
				Semaphores: []admission.SemaphoreState{
					semState("deploy", 1, admission.HoldState{Lease: "L1", Cost: 1, Capacity: 1, Superseded: true}),
				},
				Waiters: []admission.WaiterState{semWaiter("run-b", "deploy", 1, 1)},
			},
			want: []int64{0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs := tc.qs
			annotateSemaphoreETA(&qs, tc.snap)
			if len(qs.Waiters) != len(tc.want) {
				t.Fatalf("waiters = %d, want %d", len(qs.Waiters), len(tc.want))
			}
			for i, want := range tc.want {
				got := qs.Waiters[i].ExpectedStartMS
				if want == semaNone {
					if got != nil {
						t.Errorf("waiter[%d] ExpectedStartMS = %d, want nil", i, *got)
					}
					continue
				}
				if got == nil {
					t.Errorf("waiter[%d] ExpectedStartMS = nil, want %d", i, want)
					continue
				}
				if *got != want {
					t.Errorf("waiter[%d] ExpectedStartMS = %d, want %d", i, *got, want)
				}
			}
		})
	}
}

func TestMixedResourceETAUsesTheLaterKnownBoundOrUnknown(t *testing.T) {
	cases := []struct {
		name         string
		hostExpected int64
		hostElapsed  int64
		semExpected  int64
		semElapsed   int64
		wantStart    int64
		wantClear    int64
	}{
		{name: "semaphore is later than immediate host admission", hostExpected: -1, semExpected: 7000, wantStart: 7000, wantClear: 8000},
		{name: "host is later than semaphore", hostExpected: 9000, semExpected: 4000, wantStart: 9000, wantClear: 10000},
		{name: "overdue semaphore holder makes the combined estimate unknown", hostExpected: -1, semExpected: 7000, semElapsed: 7001, wantStart: semaNone, wantClear: semaNone},
		{name: "unknown host release makes the combined estimate unknown", semExpected: 4000, wantStart: semaNone, wantClear: semaNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs, snap := mixedETAState(tc.hostExpected, tc.hostElapsed, tc.semExpected, tc.semElapsed)
			annotateETA(&qs, snap)
			annotateSemaphoreETA(&qs, snap)
			assertETA(t, "ExpectedStartMS", qs.Waiters[0].ExpectedStartMS, tc.wantStart)
			assertETA(t, "ExpectedClearMS", qs.ExpectedClearMS, tc.wantClear)
		})
	}
}

func TestAtomicMultiKeyETAAllowsBackfillWithoutPartialReservation(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{{RunID: "holder-a", Semaphores: []string{"a"}, ExpectedDurationMS: 10000}},
		Waiters: []wingwire.Waiter{
			{RunID: "w1", Semaphores: []string{"a", "b"}, ExpectedDurationMS: 5000},
			{RunID: "w2", Semaphores: []string{"b"}, ExpectedDurationMS: 2000},
		},
	}
	snap := admission.Snapshot{
		Leases: []admission.LeaseState{{ID: "lease-a", RequestID: "holder-a", Claims: []admission.ClaimState{{Key: "a", Capacity: 1, Cost: 1}}}},
		Semaphores: []admission.SemaphoreState{
			semState("a", 1, semHold("lease-a", 1, 1)),
			semState("b", 1),
		},
		Waiters: []admission.WaiterState{
			{RequestID: "w1", Admit: 2, Claims: []admission.ClaimState{{Key: "a", Capacity: 1, Cost: 1}, {Key: "b", Capacity: 1, Cost: 1}}},
			{RequestID: "w2", Admit: 3, Claims: []admission.ClaimState{{Key: "b", Capacity: 1, Cost: 1}}},
		},
	}
	annotateETA(&qs, snap)
	annotateSemaphoreETA(&qs, snap)
	assertETA(t, "w1 ExpectedStartMS", qs.Waiters[0].ExpectedStartMS, 10000)
	assertETA(t, "w2 ExpectedStartMS", qs.Waiters[1].ExpectedStartMS, 0)
	assertETA(t, "ExpectedClearMS", qs.ExpectedClearMS, 15000)
}

func TestJointETAAllowsHostBackfill(t *testing.T) {
	qs := wingwire.QueueState{
		Holders: []wingwire.Holder{{RunID: "holder", Resources: wingwire.HostResources{Cores: 3}, ExpectedDurationMS: 10000}},
		Waiters: []wingwire.Waiter{
			{RunID: "large", Resources: wingwire.HostResources{Cores: 4}, ExpectedDurationMS: 5000},
			{RunID: "small", Resources: wingwire.HostResources{Cores: 1}, ExpectedDurationMS: 2000},
		},
	}
	snap := admission.Snapshot{
		TotalMilliCores: 4000, HeadroomMilliCores: 4000,
		Leases: []admission.LeaseState{{ID: "holder", RequestID: "holder", Admit: 1, MilliCores: 3000}},
		Waiters: []admission.WaiterState{
			{RequestID: "large", Admit: 2, MilliCores: 4000},
			{RequestID: "small", Admit: 3, MilliCores: 1000},
		},
	}
	annotateETA(&qs, snap)
	annotateSemaphoreETA(&qs, snap)
	assertETA(t, "large ExpectedStartMS", qs.Waiters[0].ExpectedStartMS, 10000)
	assertETA(t, "small ExpectedStartMS", qs.Waiters[1].ExpectedStartMS, 0)
	assertETA(t, "ExpectedClearMS", qs.ExpectedClearMS, 15000)
}

func TestJointETAUsesExactLargeResourceBudgets(t *testing.T) {
	const boundary = int64(1 << 53)
	for _, tc := range []struct {
		name string
		qs   wingwire.QueueState
		snap admission.Snapshot
	}{
		{
			name: "memory",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{{RunID: "holder", Resources: wingwire.HostResources{MemoryBytes: boundary + 1}, ExpectedDurationMS: 7000}},
				Waiters: []wingwire.Waiter{{RunID: "waiter", Resources: wingwire.HostResources{MemoryBytes: 2}, ExpectedDurationMS: 1000}},
			},
			snap: admission.Snapshot{
				TotalMemoryBytes: uint64(boundary + 2), HeadroomMemoryBytes: uint64(boundary + 2),
				Leases:  []admission.LeaseState{{ID: "holder", RequestID: "holder", Admit: 1, MemoryBytes: uint64(boundary + 1)}},
				Waiters: []admission.WaiterState{{RequestID: "waiter", Admit: 2, MemoryBytes: 2}},
			},
		},
		{
			name: "semaphore",
			qs: wingwire.QueueState{
				Holders: []wingwire.Holder{{RunID: "holder", Semaphores: []string{"pool"}, ExpectedDurationMS: 7000}},
				Waiters: []wingwire.Waiter{{RunID: "waiter", Semaphores: []string{"pool"}, ExpectedDurationMS: 1000}},
			},
			snap: admission.Snapshot{
				Leases:     []admission.LeaseState{{ID: "holder", RequestID: "holder", Admit: 1, Claims: []admission.ClaimState{{Key: "pool", Capacity: int(boundary + 2), Cost: int(boundary + 1)}}}},
				Semaphores: []admission.SemaphoreState{semState("pool", int(boundary+2), semHold("holder", int(boundary+2), int(boundary+1)))},
				Waiters:    []admission.WaiterState{{RequestID: "waiter", Admit: 2, Claims: []admission.ClaimState{{Key: "pool", Capacity: int(boundary + 2), Cost: 2}}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			annotateETA(&tc.qs, tc.snap)
			annotateSemaphoreETA(&tc.qs, tc.snap)
			assertETA(t, "ExpectedStartMS", tc.qs.Waiters[0].ExpectedStartMS, 7000)
			assertETA(t, "ExpectedClearMS", tc.qs.ExpectedClearMS, 8000)
		})
	}
}

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

func mixedETAState(hostExpected, hostElapsed, semExpected, semElapsed int64) (wingwire.QueueState, admission.Snapshot) {
	qs := wingwire.QueueState{Waiters: []wingwire.Waiter{{
		RunID:              "waiter",
		Resources:          wingwire.HostResources{Cores: 1},
		Semaphores:         []string{"deploy"},
		ExpectedDurationMS: 1000,
	}}}
	snap := admission.Snapshot{
		TotalMilliCores:     1000,
		TotalMemoryBytes:    1 << 30,
		HeadroomMilliCores:  1000,
		HeadroomMemoryBytes: 1 << 30,
		Waiters: []admission.WaiterState{{
			RequestID:  "waiter",
			MilliCores: 1000,
			Claims:     []admission.ClaimState{{Key: "deploy", Capacity: 1, Cost: 1}},
		}},
	}
	if hostExpected >= 0 {
		qs.Holders = append(qs.Holders, wingwire.Holder{RunID: "host-holder", Resources: wingwire.HostResources{Cores: 1}, ExpectedDurationMS: hostExpected, ElapsedMS: hostElapsed})
		snap.Leases = append(snap.Leases, admission.LeaseState{ID: "host-lease", RequestID: "host-holder", MilliCores: 1000})
	}
	qs.Holders = append(qs.Holders, wingwire.Holder{RunID: "sem-holder", Semaphores: []string{"deploy"}, ExpectedDurationMS: semExpected, ElapsedMS: semElapsed})
	snap.Leases = append(snap.Leases, admission.LeaseState{ID: "sem-lease", RequestID: "sem-holder", Claims: []admission.ClaimState{{Key: "deploy", Capacity: 1, Cost: 1}}})
	snap.Semaphores = []admission.SemaphoreState{semState("deploy", 1, semHold("sem-lease", 1, 1))}
	return qs, snap
}

func TestSemaphoreETACapacity_TakesTheSmallestDeclaration(t *testing.T) {
	snap := admission.Snapshot{
		Semaphores: []admission.SemaphoreState{semState("deploy", 4, semHold("L1", 4, 1), semHold("L2", 3, 1))},
		Waiters:    []admission.WaiterState{semWaiter("run-b", "deploy", 2, 1)},
	}
	if got := semaphoreETACapacity(snap, "deploy"); got != 2 {
		t.Fatalf("capacity = %d, want the waiter's smaller declaration 2", got)
	}
	if got := semaphoreETACapacity(snap, "missing"); got != 0 {
		t.Fatalf("capacity = %d, want 0 for a key nothing declares", got)
	}
}

// semaNone marks a want entry whose ExpectedStartMS must stay nil, since a
// nil estimate cannot be spelled in a slice of milliseconds.
const semaNone = int64(-1)

func semLease(id admission.LeaseID, runID, key string, capacity, cost int) admission.LeaseState {
	return admission.LeaseState{
		ID:        id,
		Token:     string(id) + "-token",
		RequestID: runID,
		Claims:    []admission.ClaimState{{Key: key, Capacity: capacity, Cost: cost}},
		Members:   []string{runID},
	}
}

func semState(key string, lastCapacity int, holds ...admission.HoldState) admission.SemaphoreState {
	return admission.SemaphoreState{Key: key, LastCapacity: lastCapacity, Holds: holds}
}

func semHold(lease admission.LeaseID, capacity, cost int) admission.HoldState {
	return admission.HoldState{Lease: lease, Capacity: capacity, Cost: cost}
}

func semWaiter(runID, key string, capacity, cost int) admission.WaiterState {
	return admission.WaiterState{
		RequestID: runID,
		Claims:    []admission.ClaimState{{Key: key, Capacity: capacity, Cost: cost}},
	}
}
