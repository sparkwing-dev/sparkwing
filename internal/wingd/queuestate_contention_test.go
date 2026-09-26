package wingd

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
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
	var once sync.Once
	d.cfg.Now = func() time.Time {
		once.Do(func() { close(entered) })
		return time.Now()
	}
	finished := make(chan int, 1)
	go func() { finished <- len(queueState(t, d).Waiters) }()
	<-entered
	acquired := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		d.mu.Lock()
		d.mu.Unlock()
		acquired <- time.Since(start)
	}()
	if delay := <-acquired; delay > 100*time.Millisecond {
		t.Errorf("queue calculation blocked unrelated daemon work for %s", delay)
	} else {
		t.Logf("daemon lock acquired in %s during queue calculation", delay)
	}
	if waiters := <-finished; waiters != 4000 {
		t.Fatalf("waiters = %d, want 4000", waiters)
	}
}
