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
			name: "an unknown semaphore bound clears a host-derived estimate",
			qs: wingwire.QueueState{
				Waiters: []wingwire.Waiter{
					{
						RunID:              "run-b",
						ExpectedDurationMS: 5000,
						Resources:          wingwire.HostResources{Cores: 4},
						Semaphores:         []string{"deploy"},
						ExpectedStartMS:    semStart(7000),
					},
				},
			},
			snap: admission.Snapshot{
				Semaphores: []admission.SemaphoreState{semState("deploy", 2, semHold("L1", 2, 1))},
				Waiters:    []admission.WaiterState{semWaiter("run-b", "deploy", 2, 1)},
			},
			want: []int64{semaNone},
		},
		{
			name: "a host-drawing waiter the host simulation could not estimate stays nil",
			qs: wingwire.QueueState{
				Waiters: []wingwire.Waiter{
					{
						RunID:              "run-b",
						ExpectedDurationMS: 5000,
						Resources:          wingwire.HostResources{Cores: 4},
						Semaphores:         []string{"deploy"},
					},
				},
			},
			snap: admission.Snapshot{
				Waiters: []admission.WaiterState{semWaiter("run-b", "deploy", 2, 1)},
			},
			want: []int64{semaNone},
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
	}{
		{name: "semaphore is later than immediate host admission", hostExpected: -1, semExpected: 7000, wantStart: 7000},
		{name: "host is later than semaphore", hostExpected: 9000, semExpected: 4000, wantStart: 9000},
		{name: "overdue semaphore holder makes the combined estimate unknown", hostExpected: -1, semExpected: 7000, semElapsed: 7001, wantStart: semaNone},
		{name: "unknown host release makes the combined estimate unknown", semExpected: 4000, wantStart: semaNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs, snap := mixedETAState(tc.hostExpected, tc.hostElapsed, tc.semExpected, tc.semElapsed)
			annotateETA(&qs, snap)
			annotateSemaphoreETA(&qs, snap)
			got := qs.Waiters[0].ExpectedStartMS
			if tc.wantStart == semaNone {
				if got != nil {
					t.Fatalf("ExpectedStartMS = %d, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("ExpectedStartMS = nil, want %d", tc.wantStart)
			}
			if *got != tc.wantStart {
				t.Fatalf("ExpectedStartMS = %d, want %d", *got, tc.wantStart)
			}
		})
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

func semStart(ms int64) *int64 { return &ms }

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
