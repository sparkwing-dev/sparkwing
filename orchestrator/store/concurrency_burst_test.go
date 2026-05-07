package store_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// TestConcurrency_BurstResolvesAllArrivals: fire N concurrent
// Queue-policy acquires against a Max=1 key. Each waiter should
// eventually be promoted and released, and no waiter should be
// permanently stuck. This mirrors the live HTTP burst script but
// runs in-process so failures surface clearer diagnostics.
func TestConcurrency_BurstResolvesAllArrivals(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	const N = 20
	const key = "burst-key"

	// First arrival acquires directly (Granted).
	resp, err := s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: key, HolderID: "holder-0", RunID: "run-0", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("arrival 0: %v", err)
	}
	if resp.Kind != store.AcquireGranted {
		t.Fatalf("arrival 0: want Granted, got %s", resp.Kind)
	}

	// N more arrivals queue up.
	for i := 1; i <= N; i++ {
		resp, err := s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
			Key: key, HolderID: fmt.Sprintf("holder-%d", i),
			RunID: fmt.Sprintf("run-%d", i), NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue,
		})
		if err != nil {
			t.Fatalf("arrival %d: %v", i, err)
		}
		if resp.Kind != store.AcquireQueued {
			t.Fatalf("arrival %d: want Queued, got %s", i, resp.Kind)
		}
	}

	// Drain the queue: release the current holder N+1 times, each
	// time promoting the next waiter.
	current := "holder-0"
	for step := 1; step <= N+1; step++ {
		released, err := s.ReleaseConcurrencySlot(ctx, key, current, "success", "", "", 0)
		if err != nil {
			t.Fatalf("release %d: %v", step, err)
		}
		if !released {
			t.Fatalf("release %d: not released (current=%s)", step, current)
		}
		_, err = s.ResolveCoalesceFollowers(ctx, key, "", "")
		if err != nil {
			t.Fatalf("resolve followers: %v", err)
		}
		promoted, err := s.PromoteNextWaiters(ctx, key, store.DefaultConcurrencyLease)
		if err != nil {
			t.Fatalf("promote %d: %v", step, err)
		}
		if step <= N {
			if len(promoted) != 1 {
				t.Fatalf("step %d: expected 1 promoted, got %d", step, len(promoted))
			}
			// The promoted holder_id MUST match the caller's declared holder_id.
			want := fmt.Sprintf("holder-%d", step)
			if promoted[0].HolderID != want {
				t.Fatalf("step %d: promoted holder_id = %q, want %q", step, promoted[0].HolderID, want)
			}
			current = promoted[0].HolderID
		} else {
			if len(promoted) != 0 {
				t.Fatalf("step %d: expected 0 promoted (queue drained), got %d", step, len(promoted))
			}
		}
	}

	// State must be empty.
	state, err := s.GetConcurrencyState(ctx, key)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("residual: holders=%d waiters=%d", len(state.Holders), len(state.Waiters))
	}
}

// TestConcurrency_HolderIDPreservedThroughPromotion: verifies the
// caller's custom holder_id survives queueing + promotion so a later
// heartbeat/release call using the same holder_id works.
func TestConcurrency_HolderIDPreservedThroughPromotion(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	// Leader.
	_, err := s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "k", HolderID: "leader-id", RunID: "r1", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("leader: %v", err)
	}

	// Waiter with custom holder_id.
	_, err = s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
		Key: "k", HolderID: "custom-follower", RunID: "r2", NodeID: "n",
		Capacity: 1, Policy: store.OnLimitQueue,
	})
	if err != nil {
		t.Fatalf("waiter: %v", err)
	}

	// Release + promote.
	_, _ = s.ReleaseConcurrencySlot(ctx, "k", "leader-id", "success", "", "", 0)
	promoted, _ := s.PromoteNextWaiters(ctx, "k", time.Minute)
	if len(promoted) != 1 {
		t.Fatalf("expected 1 promoted, got %d", len(promoted))
	}

	// Heartbeat with the custom holder_id must succeed.
	_, _, err = s.HeartbeatConcurrencySlot(ctx, "k", "custom-follower", time.Minute)
	if err != nil {
		t.Fatalf("heartbeat custom-follower: %v (holder_id not preserved)", err)
	}
}

// TestConcurrency_BurstConcurrentAcquireAndRelease hammers the
// primitive with N goroutines that each acquire, hold briefly, and
// release. All must complete; the store's SQLite-serialized writes
// handle the concurrency without deadlocks or stuck waiters.
func TestConcurrency_BurstConcurrentAcquireAndRelease(t *testing.T) {
	s := newStoreT(t)
	ctx := ctxT(t)

	const N = 20
	var wg sync.WaitGroup
	errs := make(chan error, N)

	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			holder := fmt.Sprintf("h-%d", i)
			runID := fmt.Sprintf("r-%d", i)

			// Single Acquire call to enter the queue (or get Granted
			// if the slot is free). Then poll ResolveWaiter — the
			// intended read-only resolution API — until we're promoted.
			resp, err := s.AcquireConcurrencySlot(ctx, store.AcquireSlotRequest{
				Key: "k", HolderID: holder, RunID: runID, NodeID: "n",
				Capacity: 1, Policy: store.OnLimitQueue,
			})
			if err != nil {
				errs <- fmt.Errorf("arrival %d acquire: %w", i, err)
				return
			}
			if resp.Kind != store.AcquireGranted {
				deadline := time.Now().Add(10 * time.Second)
				for {
					if time.Now().After(deadline) {
						errs <- fmt.Errorf("arrival %d: stuck queued past deadline", i)
						return
					}
					res, err := s.ResolveWaiter(ctx, "k", runID, "n", "", "", "")
					if err != nil {
						errs <- fmt.Errorf("arrival %d resolve: %w", i, err)
						return
					}
					if res.Status == store.WaiterPromoted {
						break
					}
					if res.Status == store.WaiterCancelled {
						errs <- fmt.Errorf("arrival %d cancelled unexpectedly", i)
						return
					}
					time.Sleep(25 * time.Millisecond)
				}
			}

			// Hold, release, promote, resolve.
			time.Sleep(5 * time.Millisecond)
			_, relErr := s.ReleaseConcurrencySlot(ctx, "k", holder, "success", "", "", 0)
			if relErr != nil {
				errs <- fmt.Errorf("arrival %d release: %w", i, relErr)
				return
			}
			_, _ = s.ResolveCoalesceFollowers(ctx, "k", "", "")
			_, _ = s.PromoteNextWaiters(ctx, "k", store.DefaultConcurrencyLease)
		}(i)
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("%v", e)
	}

	state, _ := s.GetConcurrencyState(ctx, "k")
	if len(state.Holders) != 0 || len(state.Waiters) != 0 {
		t.Fatalf("residual: holders=%d waiters=%d", len(state.Holders), len(state.Waiters))
	}
}
