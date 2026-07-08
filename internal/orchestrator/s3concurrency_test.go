package orchestrator_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// resolveTimeout bounds how long a test waits for a queued/coalesced
// waiter to resolve before failing.
const resolveTimeout = 15 * time.Second

// acquire is a thin helper that issues one AcquireSlot and fails the
// test on transport error.
func acquire(t *testing.T, c orchestrator.ConcurrencyBackend, req store.AcquireSlotRequest) store.AcquireSlotResponse {
	t.Helper()
	resp, err := c.AcquireSlot(context.Background(), req)
	if err != nil {
		t.Fatalf("AcquireSlot(%s): %v", req.Key, err)
	}
	return resp
}

// holdSlot acquires a queue-policy slot and blocks until it actually
// holds it (granted outright or promoted from the queue), returning the
// holder id. It models how the orchestrator turns a queued arrival into
// a running node.
func holdSlot(t *testing.T, c orchestrator.ConcurrencyBackend, key, runID, nodeID string, capacity, cost int) string {
	t.Helper()
	resp := acquire(t, c, store.AcquireSlotRequest{
		Key: key, RunID: runID, NodeID: nodeID,
		Capacity: capacity, Cost: cost, Policy: store.OnLimitQueue,
	})
	switch resp.Kind {
	case store.AcquireGranted:
		return resp.HolderID
	case store.AcquireQueued:
		return waitPromoted(t, c, key, runID, nodeID)
	default:
		t.Fatalf("unexpected acquire kind %q for %s/%s", resp.Kind, runID, nodeID)
		return ""
	}
}

// waitPromoted polls ResolveWaiter until the waiter is promoted to a
// holder, returning the holder id.
func waitPromoted(t *testing.T, c orchestrator.ConcurrencyBackend, key, runID, nodeID string) string {
	t.Helper()
	deadline := time.Now().Add(resolveTimeout)
	for time.Now().Before(deadline) {
		res, err := c.ResolveWaiter(context.Background(), key, runID, nodeID, "", "", "", false)
		if err != nil {
			t.Fatalf("ResolveWaiter(%s/%s): %v", runID, nodeID, err)
		}
		switch res.Status {
		case store.WaiterStillWaiting:
			time.Sleep(3 * time.Millisecond)
		case store.WaiterPromoted:
			return res.HolderID
		default:
			t.Fatalf("waiter %s/%s resolved to %q, want promoted", runID, nodeID, res.Status)
		}
	}
	t.Fatalf("waiter %s/%s never promoted within %s", runID, nodeID, resolveTimeout)
	return ""
}

func release(t *testing.T, c orchestrator.ConcurrencyBackend, key, holderID, outcome string) {
	t.Helper()
	if err := c.ReleaseSlot(context.Background(), key, holderID, outcome, "", "", 0); err != nil {
		t.Fatalf("ReleaseSlot(%s, %s): %v", key, holderID, err)
	}
}

// TestS3Concurrency_NoOverAdmission is the central guarantee: under
// sustained contention by N goroutines on one capacity-K key, the live
// holder count never exceeds K. The CAS loop is the enforcement -- two
// arrivals that both read room serialize on If-Match, and the loser
// re-reads and queues. Run under -race to surface ordering bugs.
func TestS3Concurrency_NoOverAdmission(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)

	const capacity = 3
	const workers = 12

	for round := 0; round < 3; round++ {
		key := fmt.Sprintf("g:over-admit-%d", round)
		var live atomic.Int32
		var maxLive atomic.Int32
		var ran atomic.Int32

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				runID := fmt.Sprintf("run-%d-%d", round, w)
				holderID := holdSlot(t, c, key, runID, "n", capacity, 1)

				cur := live.Add(1)
				for {
					m := maxLive.Load()
					if cur <= m || maxLive.CompareAndSwap(m, cur) {
						break
					}
				}
				if cur > capacity {
					t.Errorf("live holders = %d, exceeds capacity %d", cur, capacity)
				}
				time.Sleep(2 * time.Millisecond)
				live.Add(-1)
				ran.Add(1)
				release(t, c, key, holderID, "success")
			}(w)
		}
		wg.Wait()

		if got := maxLive.Load(); got > capacity {
			t.Fatalf("round %d: peak concurrent holders = %d, want <= %d", round, got, capacity)
		}
		if got := ran.Load(); got != workers {
			t.Fatalf("round %d: %d workers ran, want %d (some never got a slot)", round, got, workers)
		}
	}
}

// TestS3Concurrency_NoOverBudgetWithCost asserts admission is by summed
// cost, not slot count: the live holders' total cost never exceeds the
// capacity budget even when arrivals carry mixed weights.
func TestS3Concurrency_NoOverBudgetWithCost(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)

	const capacity = 5
	const workers = 15
	key := "g:over-budget"

	var liveCost atomic.Int64
	var maxCost atomic.Int64
	var ran atomic.Int32

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			cost := (w % 3) + 1 // 1, 2, 3
			runID := fmt.Sprintf("run-%d", w)
			holderID := holdSlot(t, c, key, runID, "n", capacity, cost)

			cur := liveCost.Add(int64(cost))
			for {
				m := maxCost.Load()
				if cur <= m || maxCost.CompareAndSwap(m, cur) {
					break
				}
			}
			if cur > capacity {
				t.Errorf("live cost = %d, exceeds capacity %d", cur, capacity)
			}
			time.Sleep(2 * time.Millisecond)
			liveCost.Add(-int64(cost))
			ran.Add(1)
			release(t, c, key, holderID, "success")
		}(w)
	}
	wg.Wait()

	if got := maxCost.Load(); got > capacity {
		t.Fatalf("peak live cost = %d, want <= %d", got, capacity)
	}
	if got := ran.Load(); got != workers {
		t.Fatalf("%d workers ran, want %d", got, workers)
	}
}

// TestS3Concurrency_QueueOrderingAndPromotion drives the queue policy
// deterministically: arrivals report FIFO positions, ResolveWaiter
// reflects the live queue, and each release promotes the head of line
// in arrival order.
func TestS3Concurrency_QueueOrderingAndPromotion(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "g:queue-order"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}

	for i, run := range []string{"B", "C", "D"} {
		resp := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: run, NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
		if resp.Kind != store.AcquireQueued {
			t.Fatalf("%s kind = %q, want queued", run, resp.Kind)
		}
		if resp.Position != i {
			t.Errorf("%s position = %d, want %d", run, resp.Position, i)
		}
		if resp.QueueLength != i+1 {
			t.Errorf("%s queue length = %d, want %d", run, resp.QueueLength, i+1)
		}
		if len(resp.Holders) != 1 || resp.Holders[0].RunID != "A" {
			t.Errorf("%s holders = %+v, want [A]", run, resp.Holders)
		}
	}

	// Release the head holder; the FIFO head (B) is promoted, the rest
	// shift forward.
	release(t, c, key, a.HolderID, "success")

	bHolder := waitPromoted(t, c, key, "B", "n")
	assertStillWaiting(t, c, key, "C", 0)
	assertStillWaiting(t, c, key, "D", 1)

	release(t, c, key, bHolder, "success")
	cHolder := waitPromoted(t, c, key, "C", "n")
	assertStillWaiting(t, c, key, "D", 0)

	release(t, c, key, cHolder, "success")
	dHolder := waitPromoted(t, c, key, "D", "n")
	release(t, c, key, dHolder, "success")

	_ = ctx
}

func assertStillWaiting(t *testing.T, c orchestrator.ConcurrencyBackend, key, runID string, wantPos int) {
	t.Helper()
	res, err := c.ResolveWaiter(context.Background(), key, runID, "n", "", "", "", false)
	if err != nil {
		t.Fatalf("ResolveWaiter(%s): %v", runID, err)
	}
	if res.Status != store.WaiterStillWaiting {
		t.Fatalf("%s status = %q, want still_waiting", runID, res.Status)
	}
	if res.Position != wantPos {
		t.Errorf("%s position = %d, want %d", runID, res.Position, wantPos)
	}
}

// TestS3Concurrency_SkipAndFail covers the non-queuing reject policies
// at a full slot, plus the cost-exceeds-capacity short circuit.
func TestS3Concurrency_SkipAndFail(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)

	skipKey := "g:skip"
	if a := acquire(t, c, store.AcquireSlotRequest{Key: skipKey, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitSkip}); a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	if b := acquire(t, c, store.AcquireSlotRequest{Key: skipKey, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitSkip}); b.Kind != store.AcquireSkipped {
		t.Errorf("B kind = %q, want skipped", b.Kind)
	}

	failKey := "g:fail"
	if a := acquire(t, c, store.AcquireSlotRequest{Key: failKey, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitFail}); a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	if b := acquire(t, c, store.AcquireSlotRequest{Key: failKey, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitFail}); b.Kind != store.AcquireFailed {
		t.Errorf("B kind = %q, want failed", b.Kind)
	}

	// Cost beyond capacity rejects immediately, by policy.
	if s := acquire(t, c, store.AcquireSlotRequest{Key: "g:cost-skip", RunID: "A", NodeID: "n", Capacity: 1, Cost: 2, Policy: store.OnLimitSkip}); s.Kind != store.AcquireSkipped {
		t.Errorf("cost>cap skip kind = %q, want skipped", s.Kind)
	}
	if f := acquire(t, c, store.AcquireSlotRequest{Key: "g:cost-fail", RunID: "A", NodeID: "n", Capacity: 1, Cost: 2, Policy: store.OnLimitQueue}); f.Kind != store.AcquireFailed {
		t.Errorf("cost>cap non-skip kind = %q, want failed", f.Kind)
	}
}

// TestS3Concurrency_CoalesceCacheHit asserts a coalesced follower
// inherits the leader's memoized result once the leader releases with a
// cache entry.
func TestS3Concurrency_CoalesceCacheHit(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "memo:hit"
	const hash = "content-v1"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("leader kind = %q, want granted", a.Kind)
	}
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	if b.Kind != store.AcquireCoalesced {
		t.Fatalf("follower kind = %q, want coalesced", b.Kind)
	}
	if b.LeaderRunID != "A" {
		t.Errorf("follower leader = %q, want A", b.LeaderRunID)
	}

	if err := c.ReleaseSlot(ctx, key, a.HolderID, "success", "A/n", hash, time.Minute); err != nil {
		t.Fatalf("leader release: %v", err)
	}

	res, err := c.ResolveWaiter(ctx, key, "B", "n", hash, "A", "n", false)
	if err != nil {
		t.Fatalf("resolve follower: %v", err)
	}
	if res.Status != store.WaiterCached {
		t.Fatalf("follower status = %q, want cached", res.Status)
	}
	if res.OriginRunID != "A" {
		t.Errorf("follower origin = %q, want A", res.OriginRunID)
	}
}

// TestS3Concurrency_CoalesceLeaderFailed asserts a follower whose leader
// finished without caching (a non-success outcome) inherits the
// leader's terminal outcome rather than a false success.
func TestS3Concurrency_CoalesceLeaderFailed(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "memo:leader-failed"
	const hash = "content-v2"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("leader kind = %q, want granted", a.Kind)
	}
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	if b.Kind != store.AcquireCoalesced {
		t.Fatalf("follower kind = %q, want coalesced", b.Kind)
	}

	if err := c.ReleaseSlot(ctx, key, a.HolderID, "failed", "", hash, time.Minute); err != nil {
		t.Fatalf("leader release: %v", err)
	}

	res, err := c.ResolveWaiter(ctx, key, "B", "n", hash, "A", "n", false)
	if err != nil {
		t.Fatalf("resolve follower: %v", err)
	}
	if res.Status != store.WaiterLeaderFinished {
		t.Fatalf("follower status = %q, want leader_finished", res.Status)
	}
	if res.LeaderOutcome != "failed" {
		t.Errorf("follower leader outcome = %q, want failed", res.LeaderOutcome)
	}
}

// TestS3Concurrency_CancelOthersSupersedes asserts cancel_others evicts
// the prior holder under fencing: the new arrival holds, the evicted
// holder's heartbeat reports superseded, and ForceReleaseSuperseded
// drops it.
func TestS3Concurrency_CancelOthersSupersedes(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "g:cancel-others"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitCancelOthers})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitCancelOthers})
	if b.Kind != store.AcquireCancellingOthers {
		t.Fatalf("B kind = %q, want cancelling_others", b.Kind)
	}
	if len(b.SupersededIDs) != 1 || b.SupersededIDs[0] != a.HolderID {
		t.Errorf("superseded ids = %v, want [%s]", b.SupersededIDs, a.HolderID)
	}

	_, superseded, err := c.HeartbeatSlot(ctx, key, a.HolderID, 0)
	if err != nil {
		t.Fatalf("heartbeat evicted holder: %v", err)
	}
	if !superseded {
		t.Error("evicted holder heartbeat reported superseded=false, want true")
	}
	if _, sup, err := c.HeartbeatSlot(ctx, key, b.HolderID, 0); err != nil || sup {
		t.Errorf("new holder heartbeat superseded=%v err=%v, want false/nil", sup, err)
	}

	dropped, err := c.ForceReleaseSuperseded(ctx, key)
	if err != nil {
		t.Fatalf("force release: %v", err)
	}
	if len(dropped) != 1 || dropped[0].HolderID != a.HolderID {
		t.Errorf("dropped = %+v, want [%s]", dropped, a.HolderID)
	}
}

// TestS3Concurrency_LeaseExpiryReclaimed asserts a holder whose lease
// lapsed without a heartbeat frees its budget for the next acquirer,
// and that the lapsed holder's heartbeat is refused so it cannot revive
// a slot already handed on.
func TestS3Concurrency_LeaseExpiryReclaimed(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "g:lease-expiry"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue, Lease: 80 * time.Millisecond})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}

	time.Sleep(150 * time.Millisecond)

	if _, _, err := c.HeartbeatSlot(ctx, key, a.HolderID, 0); !errors.Is(err, store.ErrLockHeld) {
		t.Errorf("heartbeat on lapsed lease err = %v, want ErrLockHeld", err)
	}

	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if b.Kind != store.AcquireGranted {
		t.Fatalf("B kind = %q, want granted (A's lapsed lease should be reclaimed)", b.Kind)
	}
}

// TestS3Concurrency_FallsBackWhenPreconditionsIgnored asserts that an
// endpoint advertising the CAS interface but ignoring preconditions
// degrades to no-op admission (every slot granted) instead of handing
// out unsafe locks.
func TestS3Concurrency_FallsBackWhenPreconditionsIgnored(t *testing.T) {
	c := orchestrator.NewS3Concurrency(&ignorePreconditionsStore{})
	key := "g:fallback"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	// Under real CAS this would queue; the no-op fallback grants it.
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if b.Kind != store.AcquireGranted {
		t.Errorf("B kind = %q, want granted (no-op fallback grants every slot)", b.Kind)
	}
}

// TestS3Concurrency_NonConditionalStoreIsNoop asserts a store without
// the ConditionalWriter capability yields the no-op backend directly.
func TestS3Concurrency_NonConditionalStoreIsNoop(t *testing.T) {
	c := orchestrator.NewS3Concurrency(&plainStore{})
	key := "g:plain"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if a.Kind != store.AcquireGranted || b.Kind != store.AcquireGranted {
		t.Errorf("kinds = %q,%q, want granted,granted (non-conditional store is no-op)", a.Kind, b.Kind)
	}
}

// --- fakes for the fallback paths ---

// plainStore is an ArtifactStore with no conditional-write capability.
type plainStore struct{}

func (*plainStore) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, storage.ErrNotFound
}
func (*plainStore) Put(context.Context, string, io.Reader) error { return nil }
func (*plainStore) Has(context.Context, string) (bool, error)    { return false, nil }
func (*plainStore) Delete(context.Context, string) error         { return nil }
func (*plainStore) List(context.Context, string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}

// ignorePreconditionsStore implements ConditionalWriter but reports that
// the endpoint does not enforce preconditions, so the concurrency
// backend must fall back to no-op coordination.
type ignorePreconditionsStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *ignorePreconditionsStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[key]
	return b, ok
}

func (s *ignorePreconditionsStore) set(key string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[key] = b
}

func (s *ignorePreconditionsStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if b, ok := s.get(key); ok {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	return nil, storage.ErrNotFound
}

func (s *ignorePreconditionsStore) Put(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.set(key, b)
	return nil
}

func (s *ignorePreconditionsStore) Has(_ context.Context, key string) (bool, error) {
	_, ok := s.get(key)
	return ok, nil
}

func (s *ignorePreconditionsStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *ignorePreconditionsStore) List(context.Context, string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}

func (s *ignorePreconditionsStore) GetWithETag(_ context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	if b, ok := s.get(key); ok {
		return io.NopCloser(bytes.NewReader(b)), storage.ETag("x"), nil
	}
	return nil, "", storage.ErrNotFound
}

func (s *ignorePreconditionsStore) PutIfAbsent(ctx context.Context, key string, r io.Reader) (storage.ETag, error) {
	return storage.ETag("x"), s.Put(ctx, key, r)
}

func (s *ignorePreconditionsStore) PutIfMatch(ctx context.Context, key string, r io.Reader, _ storage.ETag) (storage.ETag, error) {
	return storage.ETag("x"), s.Put(ctx, key, r)
}

func (*ignorePreconditionsStore) ConditionalWritesSupported(context.Context) (bool, error) {
	return false, nil
}
