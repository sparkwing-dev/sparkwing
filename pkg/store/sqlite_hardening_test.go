package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestConcurrentWriters_FailPolicyNoBusyError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")

	seed, err := teststore.OpenExisting(dbPath)
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
			s, err := teststore.OpenExisting(dbPath)
			if err != nil {
				mu.Lock()
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d open: %w", i, err))
				mu.Unlock()
				return
			}
			defer func() { _ = s.Close() }()

			createLiveRunT(t, s, fmt.Sprintf("r-%d", i))
			<-start
			resp, err := tenantFor(t, s, store.DefaultTeam).AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
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

func TestConcurrentWriters_QueuePolicyNoBusyError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed, err := teststore.OpenExisting(dbPath)
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
			s, err := teststore.OpenExisting(dbPath)
			if err != nil {
				mu.Lock()
				hardErrs = append(hardErrs, fmt.Errorf("arrival %d open: %w", i, err))
				mu.Unlock()
				return
			}
			defer func() { _ = s.Close() }()

			createLiveRunT(t, s, fmt.Sprintf("r-%d", i))
			<-start
			resp, err := tenantFor(t, s, store.DefaultTeam).AcquireConcurrencySlot(context.Background(), store.AcquireSlotRequest{
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
