package wingd

import (
	"fmt"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestDeepQueueEstimateLeavesDaemonLockAvailable(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: simulates admission of four thousand queued requests")
	}
	d := newHeadroomDaemon(t, 10, 0)
	for i := range 4002 {
		id := fmt.Sprintf("request-%d", i)
		cores := float64(1 + i%5)
		if i < 2 {
			cores = 5
		}
		request := admission.Request{ID: id, Cores: cores, MemoryBytes: uint64(1+i%3) << 30}
		if i%4 == 0 {
			request.Semaphores = []admission.SemaphoreClaim{{Key: "heavy", Capacity: 3, Cost: 1}}
		}
		if _, _, err := d.ledger.Submit(request); err != nil {
			t.Fatal(err)
		}
		d.byRun[id] = &conn{expectedDurationMS: int64(30_000 + (i%7)*20_000)}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	releaseEstimate := func() { once.Do(func() { close(release) }) }
	t.Cleanup(releaseEstimate)
	finished := make(chan int, 1)
	go func() {
		qs := d.readQueueStateWithEstimator(func(qs *wingwire.QueueState, snap admission.Snapshot) {
			close(entered)
			<-release
			annotateETA(qs, snap)
			annotateSemaphoreETA(qs, snap)
		})
		finished <- len(qs.Waiters)
	}()
	<-entered
	if !d.mu.TryLock() {
		releaseEstimate()
		<-finished
		t.Fatal("queue estimator retained the daemon mutex")
	}
	d.mu.Unlock()
	releaseEstimate()
	if waiters := <-finished; waiters != 4000 {
		t.Fatalf("waiters = %d, want 4000", waiters)
	}
}
