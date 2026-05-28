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

// TestConcurrentWriters_FailPolicyNoBusyError reproduces the failure
// mode that aborted runs under load: N separate connections (standing
// in for N `sparkwing run` processes on one host) racing to acquire the
// same capacity-1 namespace with OnLimit:Fail. Each connection opens
// its own *Store -- the SQLite-level contention only exists across
// connections, since one Store serializes through a single conn.
//
// With busy_timeout + txlock=immediate the lock contention is absorbed:
// exactly one arrival is Granted, the rest see a clean AcquireFailed
// ("slot full"), and no SQLITE_BUSY / SQLITE_BUSY_SNAPSHOT leaks
// through as an error.
func TestConcurrentWriters_FailPolicyNoBusyError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// One open up front creates the schema so the racing opens below
	// don't also contend on migration; the race we care about is the
	// acquire path.
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	_ = seed.Close()

	const N = 20
	const key = "burst-fail"

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		granted  int
		failed   int
		hardErrs []error
	)
	start := make(chan struct{})

	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := store.Open(dbPath)
			if err != nil {
				mu.Lock()
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d open: %w", i, err))
				mu.Unlock()
				return
			}
			defer func() { _ = s.Close() }()

			<-start
			resp, err := s.AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
				Key: key, HolderID: fmt.Sprintf("h-%d", i),
				RunID: fmt.Sprintf("r-%d", i), NodeID: "n",
				Capacity: 1, Policy: store.OnLimitFail,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d acquire: %w", i, err))
			case resp.Kind == store.AcquireGranted:
				granted++
			case resp.Kind == store.AcquireFailed:
				failed++
			default:
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d: unexpected kind %s", i, resp.Kind))
			}
		}(i)
	}

	close(start)
	wg.Wait()

	for _, e := range hardErrs {
		t.Errorf("%v", e)
	}
	if granted != 1 {
		t.Errorf("granted = %d, want exactly 1", granted)
	}
	if failed != N-1 {
		t.Errorf("slot-full failures = %d, want %d", failed, N-1)
	}
}

// TestConcurrentWriters_QueuePolicyNoBusyError mirrors the Fail test
// but with OnLimit:Queue: one Granted, the rest Queued, across separate
// connections. No lock-contention error should surface.
func TestConcurrentWriters_QueuePolicyNoBusyError(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	_ = seed.Close()

	const N = 20
	const key = "burst-queue"

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		granted  int
		queued   int
		hardErrs []error
	)
	start := make(chan struct{})

	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := store.Open(dbPath)
			if err != nil {
				mu.Lock()
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d open: %w", i, err))
				mu.Unlock()
				return
			}
			defer func() { _ = s.Close() }()

			<-start
			resp, err := s.AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
				Key: key, HolderID: fmt.Sprintf("h-%d", i),
				RunID: fmt.Sprintf("r-%d", i), NodeID: "n",
				Capacity: 1, Policy: store.OnLimitQueue,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d acquire: %w", i, err))
			case resp.Kind == store.AcquireGranted:
				granted++
			case resp.Kind == store.AcquireQueued:
				queued++
			default:
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d: unexpected kind %s", i, resp.Kind))
			}
		}(i)
	}

	close(start)
	wg.Wait()

	for _, e := range hardErrs {
		t.Errorf("%v", e)
	}
	if granted != 1 {
		t.Errorf("granted = %d, want exactly 1", granted)
	}
	if queued != N-1 {
		t.Errorf("queued = %d, want %d", queued, N-1)
	}
}

// TestOpenReadOnly_RejectsWrites: a store opened read-only must refuse
// to mutate state so the dashboard daemon can't take a write lock.
func TestOpenReadOnly_RejectsWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	rw, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	_ = rw.Close()

	ro, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()

	err = ro.CreateRun(context.Background(), store.Run{
		ID: "r1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	})
	if err == nil {
		t.Fatal("CreateRun on read-only store: want error, got nil")
	}
}

// TestOpenReadOnly_DoesNotBlockWriter: a read-only consumer holding an
// open read transaction must not stop a separate writer from
// committing. WAL + query_only guarantees this; the assertion is that
// the write lands well inside busy_timeout.
func TestOpenReadOnly_DoesNotBlockWriter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	rw, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open rw: %v", err)
	}
	defer func() { _ = rw.Close() }()
	// Seed a row so the read transaction has something to read.
	if err := rw.CreateRun(context.Background(), store.Run{
		ID: "seed", Pipeline: "p", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	ro, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer func() { _ = ro.Close() }()

	// Hold a read transaction open on the read-only connection.
	readTx, err := ro.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer func() { _ = readTx.Rollback() }()
	var n int
	if err := readTx.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		t.Fatalf("read query: %v", err)
	}

	// The writer must commit promptly despite the held reader.
	done := make(chan error, 1)
	go func() {
		done <- rw.CreateRun(context.Background(), store.Run{
			ID: "w1", Pipeline: "p", Status: "running", StartedAt: time.Now(),
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writer blocked by read-only reader: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not commit within 5s; read-only reader is blocking it")
	}
}
