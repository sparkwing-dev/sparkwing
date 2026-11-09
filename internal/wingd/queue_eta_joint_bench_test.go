package wingd

import (
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func etaBenchQueue(waiters int) (wingwire.QueueState, admission.Snapshot) {
	ledger, err := admission.New(admission.Config{TotalCores: 10, TotalMemoryBytes: 16 << 30})
	if err != nil {
		panic(err)
	}
	var qs wingwire.QueueState
	for i := range waiters + 2 {
		id := fmt.Sprintf("request-%d", i)
		cores := float64(1 + i%5)
		if i < 2 {
			cores = 5
		}
		request := admission.Request{ID: id, Cores: cores, MemoryBytes: uint64(1+i%3) << 30}
		if i%4 == 0 {
			request.Semaphores = []admission.SemaphoreClaim{{Key: "heavy", Capacity: 3, Cost: 1}}
		}
		if _, _, err := ledger.Submit(request); err != nil {
			panic(err)
		}
		if i < 2 {
			qs.Holders = append(qs.Holders, wingwire.Holder{RunID: id, ExpectedDurationMS: 120_000, ElapsedMS: 30_000})
		} else {
			qs.Waiters = append(qs.Waiters, wingwire.Waiter{RunID: id, ExpectedDurationMS: int64(30_000 + (i%7)*20_000)})
		}
	}
	return qs, ledger.Snapshot()
}

func BenchmarkSemaphoreETA(b *testing.B) {
	for _, n := range []int{25, 50, 100, 205, 400} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			qs, snap := etaBenchQueue(n)
			for i := 0; i < b.N; i++ {
				q := qs
				q.Waiters = append([]wingwire.Waiter(nil), qs.Waiters...)
				annotateSemaphoreETA(&q, snap)
			}
		})
	}
}
