package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const (
	seqAppenders       = 8
	seqAppendsEach     = 20
	seqAppendsExpected = seqAppenders * seqAppendsEach
)

func appendEventsConcurrently(t *testing.T, st *store.Store, runID string) int {
	t.Helper()
	ctx := context.Background()
	var mu sync.Mutex
	landed := 0
	var wg sync.WaitGroup
	for i := 0; i < seqAppenders; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < seqAppendsEach; j++ {
				if err := st.AppendEvent(ctx, runID, fmt.Sprintf("n-%d", worker), "log", nil); err != nil {
					t.Errorf("AppendEvent(worker %d, append %d): %v", worker, j, err)
					return
				}
				mu.Lock()
				landed++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return landed
}

func checkAppendedSeqs(t *testing.T, st *store.Store, runID string, landed int) {
	t.Helper()
	if landed != seqAppendsExpected {
		t.Fatalf("appends that landed = %d, want %d", landed, seqAppendsExpected)
	}
	events, err := teststore.ForTeam(t, st, store.DefaultTeam).ListEventsAfter(context.Background(), runID, 0, seqAppendsExpected+1)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	if len(events) != seqAppendsExpected {
		t.Errorf("stored events = %d, want %d", len(events), seqAppendsExpected)
	}
	seen := make(map[int64]bool, len(events))
	for _, ev := range events {
		if seen[ev.Seq] {
			t.Errorf("seq %d handed out twice", ev.Seq)
		}
		seen[ev.Seq] = true
	}
}

func TestAppendEventConcurrentAppendsAllLand(t *testing.T) {
	st, err := storetest.New(t).TryOpen()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	checkAppendedSeqs(t, st, "run-1", appendEventsConcurrently(t, st, "run-1"))
}

// The seq an append allocates is half the events primary key, so two
// appenders that read the same MAX(seq) produce one insert that dies on the
// duplicate. Postgres runs at READ COMMITTED, where only a lock on the run
// row keeps that from happening; SQLite's immediate transactions hide it.
func TestPostgresAppendEventConcurrentAppendsAllLand(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	if err := teststore.ForTeam(t, st, store.DefaultTeam).TestOnlySeedRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	checkAppendedSeqs(t, st, "run-1", appendEventsConcurrently(t, st, "run-1"))
}
