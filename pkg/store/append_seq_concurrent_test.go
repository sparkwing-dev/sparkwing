package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	seqAppenders       = 8
	seqAppendsEach     = 20
	seqAppendsExpected = seqAppenders * seqAppendsEach
)

func appendEventsConcurrently(t *testing.T, st *store.Store, runID string) []int64 {
	t.Helper()
	ctx := context.Background()
	var mu sync.Mutex
	var seqs []int64
	var wg sync.WaitGroup
	for i := 0; i < seqAppenders; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < seqAppendsEach; j++ {
				seq, err := st.AppendEvent(ctx, runID, fmt.Sprintf("n-%d", worker), "log", nil)
				if err != nil {
					t.Errorf("AppendEvent(worker %d, append %d): %v", worker, j, err)
					return
				}
				mu.Lock()
				seqs = append(seqs, seq)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	return seqs
}

func checkAppendedSeqs(t *testing.T, st *store.Store, runID string, seqs []int64) {
	t.Helper()
	if len(seqs) != seqAppendsExpected {
		t.Fatalf("appends that returned a seq = %d, want %d", len(seqs), seqAppendsExpected)
	}
	seen := make(map[int64]bool, len(seqs))
	for _, seq := range seqs {
		if seen[seq] {
			t.Errorf("seq %d handed out twice", seq)
		}
		seen[seq] = true
	}
	events, err := st.ListEventsAfter(context.Background(), runID, 0, seqAppendsExpected+1)
	if err != nil {
		t.Fatalf("ListEventsAfter: %v", err)
	}
	if len(events) != seqAppendsExpected {
		t.Errorf("stored events = %d, want %d", len(events), seqAppendsExpected)
	}
}

func TestAppendEventConcurrentAppendsAllLand(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
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
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	checkAppendedSeqs(t, st, "run-1", appendEventsConcurrently(t, st, "run-1"))
}

// RequestNodeBounce allocates its seq the same way against the same kind of
// primary key, so it needs the same lock.
func TestPostgresRequestNodeBounceConcurrentRequestsAllLand(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{
		ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := st.StartNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("StartNode: %v", err)
	}

	const requests = 8
	var mu sync.Mutex
	seen := map[int64]bool{}
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			b, err := st.RequestNodeBounce(ctx, "run-1", "build", fmt.Sprintf("op-%d", worker))
			if err != nil {
				t.Errorf("RequestNodeBounce(%d): %v", worker, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[b.Seq] {
				t.Errorf("bounce seq %d handed out twice", b.Seq)
			}
			seen[b.Seq] = true
		}(i)
	}
	wg.Wait()

	if len(seen) != requests {
		t.Errorf("distinct bounce seqs = %d, want %d", len(seen), requests)
	}
}
