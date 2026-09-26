package wingd

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func etaBenchQueue(waiters int) (wingwire.QueueState, admission.Snapshot) {
	snap := admission.Snapshot{TotalMilliCores: 10_000, HeadroomMilliCores: 10_000, TotalMemoryBytes: 16 << 30, HeadroomMemoryBytes: 16 << 30}
	var qs wingwire.QueueState
	for i := 0; i < 2; i++ {
		id := admission.LeaseID(fmt.Sprintf("L%d", i))
		run := fmt.Sprintf("holder-%d", i)
		lease := semLease(id, run, "heavy", 3, 1)
		lease.MilliCores = 5000
		lease.MemoryBytes = 2 << 30
		snap.Leases = append(snap.Leases, lease)
		qs.Holders = append(qs.Holders, wingwire.Holder{RunID: run, ExpectedDurationMS: 120_000, ElapsedMS: 30_000, Semaphores: []string{"heavy"}})
	}
	snap.Semaphores = []admission.SemaphoreState{semState("heavy", 3, semHold("L0", 3, 1), semHold("L1", 3, 1))}
	for i := 0; i < waiters; i++ {
		run := fmt.Sprintf("waiter-%d", i)
		w := admission.WaiterState{RequestID: run, MilliCores: int64(1000 + (i%5)*1000), MemoryBytes: uint64(1+i%3) << 30}
		sems := []string(nil)
		if i%4 == 0 {
			w.Claims = []admission.ClaimState{{Key: "heavy", Capacity: 3, Cost: 1}}
			sems = []string{"heavy"}
		}
		snap.Waiters = append(snap.Waiters, w)
		qs.Waiters = append(qs.Waiters, wingwire.Waiter{RunID: run, ExpectedDurationMS: int64(30_000 + (i%7)*20_000), Semaphores: sems})
	}
	return qs, snap
}

func BenchmarkSemaphoreETA(b *testing.B) {
	for _, n := range []int{25, 50, 100, 205, 400} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			qs, snap := etaBenchQueue(n)
			for i := 0; i < b.N; i++ {
				q := qs
				q.Waiters = append([]wingwire.Waiter(nil), qs.Waiters...)
				annotateETA(&q, snap)
				annotateSemaphoreETA(&q, snap)
			}
		})
	}
}
