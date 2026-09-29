package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type blockingCounter struct {
	calls   atomic.Int32
	release chan struct{}
}

func (b *blockingCounter) Team() store.Team { return "acme" }

func (b *blockingCounter) CountPendingTriggers(context.Context) (int, error) {
	b.calls.Add(1)
	<-b.release
	return 7, nil
}

// A burst misses the depth cache together; one count answers all of it.
func TestQueueDepthCache_ABurstOfMissesCountsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q := &queueDepthCache{teams: make(map[store.Team]queueDepth)}
		counter := &blockingCounter{release: make(chan struct{})}
		now := time.Now()
		var wg sync.WaitGroup
		depths := make([]int, 8)
		for i := range depths {
			wg.Go(func() {
				depth, err := q.read(context.Background(), counter, now)
				if err != nil {
					t.Errorf("read: %v", err)
				}
				depths[i] = depth
			})
		}
		synctest.Wait()
		close(counter.release)
		wg.Wait()
		if n := counter.calls.Load(); n != 1 {
			t.Errorf("a burst of %d misses counted %d times, want once", len(depths), n)
		}
		for i, depth := range depths {
			if depth != 7 {
				t.Errorf("submission %d read depth %d, want 7", i, depth)
			}
		}
	})
}
