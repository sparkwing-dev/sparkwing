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

const resolveTimeout = 15 * time.Second

func acquire(t *testing.T, c orchestrator.ConcurrencyBackend, req store.AcquireSlotRequest) store.AcquireSlotResponse {
	t.Helper()
	resp, err := c.AcquireSlot(context.Background(), req)
	if err != nil {
		t.Fatalf("AcquireSlot(%s): %v", req.Key, err)
	}
	return resp
}

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

func waitPromoted(t *testing.T, c orchestrator.ConcurrencyBackend, key, runID, nodeID string) string {
	t.Helper()
	deadline := time.Now().Add(resolveTimeout)
	poll := time.NewTicker(3 * time.Millisecond)
	defer poll.Stop()
	for time.Now().Before(deadline) {
		res, err := c.ResolveWaiter(context.Background(), key, runID, nodeID, "", "", "", false)
		if err != nil {
			t.Fatalf("ResolveWaiter(%s/%s): %v", runID, nodeID, err)
		}
		switch res.Status {
		case store.WaiterStillWaiting:
			<-poll.C
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

func runS3ConcurrencyBurst(t *testing.T, c orchestrator.ConcurrencyBackend, key string, capacity int, costs []int) (int, int64, int32) {
	t.Helper()
	type initialResult struct {
		resp store.AcquireSlotResponse
		cost int
		err  error
	}
	type admission struct {
		worker  int
		cost    int
		release chan struct{}
	}
	type waiterCheck struct {
		worker int
		resume chan struct{}
	}
	initial := make(chan initialResult, len(costs))
	admitted := make(chan admission, len(costs))
	checked := make(chan waiterCheck, len(costs)*2)
	finished := make(chan int, len(costs))
	workerErrors := make(chan error, len(costs))
	start := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int32

	var wg sync.WaitGroup
	for w, cost := range costs {
		wg.Add(1)
		go func(w, cost int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-%d", w)
			<-start
			resp, err := c.AcquireSlot(ctx, store.AcquireSlotRequest{
				Key: key, RunID: runID, NodeID: "n",
				Capacity: capacity, Cost: cost, Policy: store.OnLimitQueue,
			})
			initial <- initialResult{resp: resp, cost: cost, err: err}
			if err != nil {
				return
			}
			holderID := resp.HolderID
			switch resp.Kind {
			case store.AcquireGranted:
			case store.AcquireQueued:
				deadline := time.Now().Add(resolveTimeout)
				poll := time.NewTicker(3 * time.Millisecond)
				defer poll.Stop()
				for time.Now().Before(deadline) {
					resolved, err := c.ResolveWaiter(ctx, key, runID, "n", "", "", "", false)
					if err != nil {
						workerErrors <- fmt.Errorf("ResolveWaiter(%s/n): %w", runID, err)
						return
					}
					if resolved.Status == store.WaiterPromoted {
						holderID = resolved.HolderID
						break
					}
					if resolved.Status != store.WaiterStillWaiting {
						workerErrors <- fmt.Errorf("waiter %s/n resolved to %q, want promoted", runID, resolved.Status)
						return
					}
					resume := make(chan struct{})
					select {
					case checked <- waiterCheck{worker: w, resume: resume}:
					case <-ctx.Done():
						return
					}
					select {
					case <-resume:
					case <-ctx.Done():
						return
					}
					<-poll.C
				}
				if holderID == "" {
					workerErrors <- fmt.Errorf("waiter %s/n never promoted within %s", runID, resolveTimeout)
					return
				}
			default:
				workerErrors <- fmt.Errorf("unexpected acquire kind %q for %s/n", resp.Kind, runID)
				return
			}

			permit := make(chan struct{})
			admitted <- admission{worker: w, cost: cost, release: permit}
			select {
			case <-permit:
			case <-ctx.Done():
				return
			}
			ran.Add(1)
			if err := c.ReleaseSlot(ctx, key, holderID, "success", "", "", 0); err != nil {
				workerErrors <- fmt.Errorf("ReleaseSlot(%s, %s): %w", key, holderID, err)
				return
			}
			finished <- w
		}(w, cost)
	}
	joined := make(chan struct{})
	go func() {
		wg.Wait()
		close(joined)
	}()
	defer func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-joined:
		case <-timer.C:
			t.Errorf("S3 concurrency burst workers did not stop")
		}
	}()

	close(start)
	grantedCost := 0
	var acquireErr error
	for range costs {
		result := <-initial
		if result.err != nil && acquireErr == nil {
			acquireErr = result.err
		}
		if result.resp.Kind == store.AcquireGranted {
			grantedCost += result.cost
		}
	}
	if acquireErr != nil {
		t.Fatalf("initial acquisition: %v", acquireErr)
	}

	remaining := make(map[int]bool, len(costs))
	for worker := range costs {
		remaining[worker] = true
	}
	held := make(map[int]admission, capacity)
	var heldCost int64
	var maxHeldCost int64
	for len(remaining) > 0 || len(held) > 0 {
		stableWaiters := make(map[int]chan struct{}, len(remaining))
		for len(stableWaiters) < len(remaining) {
			select {
			case err := <-workerErrors:
				t.Fatalf("S3 concurrency burst worker: %v", err)
			case entry := <-admitted:
				if !remaining[entry.worker] {
					t.Fatalf("worker %d admitted more than once", entry.worker)
				}
				delete(remaining, entry.worker)
				held[entry.worker] = entry
				heldCost += int64(entry.cost)
			case check := <-checked:
				if remaining[check.worker] {
					if _, exists := stableWaiters[check.worker]; exists {
						t.Fatalf("worker %d reported stable waiting more than once", check.worker)
					}
					stableWaiters[check.worker] = check.resume
				} else {
					close(check.resume)
				}
			}
		}
		if heldCost > int64(capacity) {
			t.Fatalf("live holder cost = %d, exceeds capacity %d", heldCost, capacity)
		}
		if heldCost > maxHeldCost {
			maxHeldCost = heldCost
		}

		var released admission
		for worker, entry := range held {
			released = entry
			delete(held, worker)
			break
		}
		if released.release != nil {
			close(released.release)
			select {
			case <-finished:
				heldCost -= int64(released.cost)
			case err := <-workerErrors:
				t.Fatalf("S3 concurrency burst worker: %v", err)
			}
		}
		for _, resume := range stableWaiters {
			close(resume)
		}
	}
	<-joined
	select {
	case err := <-workerErrors:
		t.Fatalf("S3 concurrency burst worker: %v", err)
	default:
	}
	return grantedCost, maxHeldCost, ran.Load()
}

func TestS3Concurrency_NoOverAdmission(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)

	const capacity = 3
	const workers = 12

	for round := 0; round < 3; round++ {
		key := fmt.Sprintf("g:over-admit-%d", round)
		costs := make([]int, workers)
		for i := range costs {
			costs[i] = 1
		}
		grantedCost, maxCost, ran := runS3ConcurrencyBurst(t, c, key, capacity, costs)
		if grantedCost != capacity {
			t.Fatalf("initial burst granted cost %d, want %d", grantedCost, capacity)
		}
		if maxCost > capacity {
			t.Fatalf("round %d: peak live cost = %d, want <= %d", round, maxCost, capacity)
		}
		if ran != workers {
			t.Fatalf("round %d: %d workers ran, want %d (some never got a slot)", round, ran, workers)
		}
	}
}

func TestS3Concurrency_NoOverBudgetWithCost(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)

	const capacity = 5
	const workers = 15
	key := "g:over-budget"

	costs := make([]int, workers)
	for w := range costs {
		costs[w] = (w % 3) + 1
	}
	grantedCost, maxCost, ran := runS3ConcurrencyBurst(t, c, key, capacity, costs)
	if grantedCost > capacity {
		t.Fatalf("initial burst granted cost %d, exceeds capacity %d", grantedCost, capacity)
	}
	if maxCost > capacity {
		t.Fatalf("peak live cost = %d, want <= %d", maxCost, capacity)
	}
	if ran != workers {
		t.Fatalf("%d workers ran, want %d", ran, workers)
	}
}

func TestS3Concurrency_CostBackfillsBehindNonFittingHeavyWaiter(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:cost-backfill"

	holder := holdSlot(t, c, key, "holder", "n", 8, 6)
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want queued got %s", r.Kind)
	}
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "light/n", RunID: "light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireGranted {
		t.Fatalf("light: want granted as backfill got %s", r.Kind)
	}

	release(t, c, key, holder, "success")
	heavyHolder := waitPromoted(t, c, key, "heavy", "n")
	release(t, c, key, heavyHolder, "success")
}

func TestS3Concurrency_CostPromotionBackfillsBehindNonFittingHeavyWaiter(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:cost-promotion-backfill"

	holderHeavy := holdSlot(t, c, key, "holder-heavy", "n", 8, 6)
	holderLight := holdSlot(t, c, key, "holder-light", "n", 8, 2)
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 8, Cost: 6, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want queued got %s", r.Kind)
	}
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "light/n", RunID: "light", NodeID: "n",
		Capacity: 8, Cost: 2, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("light: want queued got %s", r.Kind)
	}

	release(t, c, key, holderLight, "success")
	lightHolder := waitPromoted(t, c, key, "light", "n")
	assertStillWaiting(t, c, key, "heavy", 0)
	release(t, c, key, holderHeavy, "success")
	heavyHolder := waitPromoted(t, c, key, "heavy", "n")
	release(t, c, key, lightHolder, "success")
	release(t, c, key, heavyHolder, "success")
}

func TestS3Concurrency_CostBackfillStopsWhenYoungerHolderBlocksOldestWaiter(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:cost-backfill-bound"

	older := holdSlot(t, c, key, "older", "n", 10, 5)
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "heavy/n", RunID: "heavy", NodeID: "n",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("heavy: want queued got %s", r.Kind)
	}
	lightOne := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "light-1/n", RunID: "light-1", NodeID: "n",
		Capacity: 10, Cost: 5, Policy: store.OnLimitQueue,
	})
	if lightOne.Kind != store.AcquireGranted {
		t.Fatalf("light-1: want granted as backfill got %s", lightOne.Kind)
	}
	if r := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "light-2/n", RunID: "light-2", NodeID: "n",
		Capacity: 10, Cost: 5, Policy: store.OnLimitQueue,
	}); r.Kind != store.AcquireQueued {
		t.Fatalf("light-2: want queued behind protected heavy got %s", r.Kind)
	}

	release(t, c, key, older, "success")
	assertStillWaiting(t, c, key, "heavy", 0)
	assertStillWaiting(t, c, key, "light-2", 1)
	release(t, c, key, lightOne.HolderID, "success")
	heavyHolder := waitPromoted(t, c, key, "heavy", "n")
	release(t, c, key, heavyHolder, "success")
}

func TestS3Concurrency_ResolveWaiterPromotesAfterHolderLeaseExpires(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:resolve-expired-holder"

	a := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "A/n",
		RunID:    "A",
		NodeID:   "n",
		Capacity: 1,
		Cost:     1,
		Policy:   store.OnLimitQueue,
		Lease:    20 * time.Millisecond,
	})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	b := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "B/n",
		RunID:    "B",
		NodeID:   "n",
		Capacity: 1,
		Cost:     1,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if b.Kind != store.AcquireQueued {
		t.Fatalf("B kind = %q, want queued", b.Kind)
	}
	if holderID := waitPromoted(t, c, key, "B", "n"); holderID != "B/n" {
		t.Fatalf("holder = %q, want B/n", holderID)
	}
}

func TestS3Concurrency_InheritedHolderExtendsAdmissionWithoutRechargingCost(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:inherited-budget"

	parent := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}

	child := acquire(t, c, store.AcquireSlotRequest{
		Key:               key,
		HolderID:          "child/-",
		InheritedHolderID: "parent/-",
		RunID:             "child",
		Capacity:          10,
		Cost:              20,
		Policy:            store.OnLimitQueue,
		Lease:             2 * time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child: want Granted got %s", child.Kind)
	}
	if child.HolderID != "child/-" {
		t.Fatalf("child holder = %q, want child holder", child.HolderID)
	}

	parentAfter, err := c.ObserveSlot(context.Background(), key, "parent/-")
	if err != nil {
		t.Fatalf("ObserveSlot(parent): %v", err)
	}
	if !parentAfter.LeaseExpiresAt.After(parent.LeaseExpiresAt) {
		t.Fatalf("inherited join did not extend parent lease: before=%s after=%s",
			parent.LeaseExpiresAt, parentAfter.LeaseExpiresAt)
	}
	childHolder, err := c.ObserveSlot(context.Background(), key, "child/-")
	if err != nil {
		t.Fatalf("ObserveSlot(child): %v", err)
	}
	if childHolder.Cost != 0 {
		t.Fatalf("child holder cost = %d, want zero before parent release", childHolder.Cost)
	}

	if err := c.ReleaseSlot(context.Background(), key, "parent/-", "success", "", "", 0); err != nil {
		t.Fatalf("ReleaseSlot(parent): %v", err)
	}
	childHolder, err = c.ObserveSlot(context.Background(), key, "child/-")
	if err != nil {
		t.Fatalf("ObserveSlot(child after parent release): %v", err)
	}
	if childHolder.Cost != 8 {
		t.Fatalf("child holder cost after parent release = %d, want transferred 8", childHolder.Cost)
	}
	if childHolder.NodeID != "" {
		t.Fatalf("child node id after parent release = %q, want empty plan holder node", childHolder.NodeID)
	}

	follower := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "follower/-",
		RunID:    "follower",
		Capacity: 10,
		Cost:     3,
		Policy:   store.OnLimitQueue,
	})
	if follower.Kind != store.AcquireQueued {
		t.Fatalf("follower: want Queued because parent still accounts for cost 8, got %s", follower.Kind)
	}
}

func TestS3Concurrency_CancelOthersSupersedesInheritedHolder(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:inherited-cancel-others"

	parent := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "parent/-",
		RunID:    "parent",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitQueue,
		Lease:    time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	child := acquire(t, c, store.AcquireSlotRequest{
		Key:               key,
		HolderID:          "child/-",
		InheritedHolderID: "parent/-",
		RunID:             "child",
		Capacity:          10,
		Cost:              8,
		Policy:            store.OnLimitQueue,
		Lease:             time.Minute,
	})
	if child.Kind != store.AcquireGranted {
		t.Fatalf("child: want Granted got %s", child.Kind)
	}

	evictor := acquire(t, c, store.AcquireSlotRequest{
		Key:      key,
		HolderID: "evictor/-",
		RunID:    "evictor",
		Capacity: 10,
		Cost:     8,
		Policy:   store.OnLimitCancelOthers,
		Lease:    time.Minute,
	})
	if evictor.Kind != store.AcquireCancellingOthers {
		t.Fatalf("evictor: want CancellingOthers got %s", evictor.Kind)
	}
	if len(evictor.SupersededIDs) != 2 || evictor.SupersededIDs[0] != "parent/-" || evictor.SupersededIDs[1] != "child/-" {
		t.Fatalf("superseded ids = %v, want parent and child", evictor.SupersededIDs)
	}
	_, superseded, err := c.HeartbeatSlot(context.Background(), key, "child/-", time.Minute)
	if err != nil {
		t.Fatalf("HeartbeatSlot(child): %v", err)
	}
	if !superseded {
		t.Fatal("child heartbeat did not report superseded")
	}
}

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
	if res.QueueLength < wantPos+1 {
		t.Errorf("%s queue length = %d, want at least %d", runID, res.QueueLength, wantPos+1)
	}
}

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

	if s := acquire(t, c, store.AcquireSlotRequest{Key: "g:cost-skip", RunID: "A", NodeID: "n", Capacity: 1, Cost: 2, Policy: store.OnLimitSkip}); s.Kind != store.AcquireSkipped {
		t.Errorf("cost>cap skip kind = %q, want skipped", s.Kind)
	}
	if f := acquire(t, c, store.AcquireSlotRequest{Key: "g:cost-fail", RunID: "A", NodeID: "n", Capacity: 1, Cost: 2, Policy: store.OnLimitQueue}); f.Kind != store.AcquireFailed {
		t.Errorf("cost>cap non-skip kind = %q, want failed", f.Kind)
	}
}

func TestS3Concurrency_CancelOthersSupersedesSiblingInheritedHolders(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	key := "g:inherited-siblings"
	parent := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "parent/-", RunID: "parent",
		Capacity: 10, Cost: 8, Policy: store.OnLimitQueue, Lease: time.Minute,
	})
	if parent.Kind != store.AcquireGranted {
		t.Fatalf("parent: want Granted got %s", parent.Kind)
	}
	for _, childRunID := range []string{"child-a", "child-b"} {
		child := acquire(t, c, store.AcquireSlotRequest{
			Key: key, HolderID: childRunID + "/-", InheritedHolderID: "parent/-", RunID: childRunID,
			Capacity: 10, Cost: 20, Policy: store.OnLimitQueue, Lease: 2 * time.Minute,
		})
		if child.Kind != store.AcquireGranted {
			t.Fatalf("%s: want Granted got %s", childRunID, child.Kind)
		}
	}
	release(t, c, key, "parent/-", "success")

	evictor := acquire(t, c, store.AcquireSlotRequest{
		Key: key, HolderID: "evictor/-", RunID: "evictor",
		Capacity: 10, Cost: 8, Policy: store.OnLimitCancelOthers, Lease: time.Minute,
	})
	if evictor.Kind != store.AcquireCancellingOthers {
		t.Fatalf("evictor: want CancellingOthers got %s", evictor.Kind)
	}
	if !containsString(evictor.SupersededIDs, "child-a/-") || !containsString(evictor.SupersededIDs, "child-b/-") {
		t.Fatalf("superseded ids = %+v, want both inherited siblings", evictor.SupersededIDs)
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

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

func TestS3Concurrency_CoalesceFollowerPromotesAfterFailedLeader(t *testing.T) {
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
	if res.Status != store.WaiterPromoted {
		t.Fatalf("follower status = %q, want promoted", res.Status)
	}
	if res.HolderID != "B/n" {
		t.Errorf("follower holder = %q, want B/n", res.HolderID)
	}
}

func TestS3Concurrency_CoalesceFollowersReparentAfterFailedLeader(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "memo:leader-failed-reparent"
	const hash = "content-v3"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	cFollower := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "C", NodeID: "n", Capacity: 1, Policy: store.OnLimitCoalesce, CacheKeyHash: hash})
	if a.Kind != store.AcquireGranted || b.Kind != store.AcquireCoalesced || cFollower.Kind != store.AcquireCoalesced {
		t.Fatalf("acquire kinds = %q, %q, %q; want granted, coalesced, coalesced", a.Kind, b.Kind, cFollower.Kind)
	}
	if err := c.ReleaseSlot(ctx, key, a.HolderID, "failed", "", hash, time.Minute); err != nil {
		t.Fatalf("failed leader release: %v", err)
	}

	bResolution, err := c.ResolveWaiter(ctx, key, "B", "n", hash, "A", "n", false)
	if err != nil {
		t.Fatalf("resolve first follower: %v", err)
	}
	if bResolution.Status != store.WaiterPromoted {
		t.Fatalf("first follower status = %q, want promoted", bResolution.Status)
	}
	cResolution, err := c.ResolveWaiter(ctx, key, "C", "n", hash, "A", "n", false)
	if err != nil {
		t.Fatalf("resolve second follower: %v", err)
	}
	if cResolution.Status != store.WaiterStillWaiting {
		t.Fatalf("second follower status = %q, want still_waiting", cResolution.Status)
	}

	if err := c.ReleaseSlot(ctx, key, bResolution.HolderID, "success", "B/n", hash, time.Minute); err != nil {
		t.Fatalf("promoted follower release: %v", err)
	}
	cResolution, err = c.ResolveWaiter(ctx, key, "C", "n", hash, "B", "n", false)
	if err != nil {
		t.Fatalf("resolve second follower after success: %v", err)
	}
	if cResolution.Status != store.WaiterCached || cResolution.OriginRunID != "B" {
		t.Fatalf("second follower resolution = %+v, want cached result from B", cResolution)
	}
}

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

func TestS3Concurrency_LeaseExpiryReclaimed(t *testing.T) {
	art, _ := openIntegrationS3(t)
	c := orchestrator.NewS3Concurrency(art)
	ctx := context.Background()
	key := "g:lease-expiry"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue, Lease: 80 * time.Millisecond})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}

	if err := orchestrator.ExpireS3ConcurrencyHolderForTest(ctx, c, key, a.HolderID); err != nil {
		t.Fatalf("expire holder A: %v", err)
	}

	if _, _, err := c.HeartbeatSlot(ctx, key, a.HolderID, 0); !errors.Is(err, store.ErrLockHeld) {
		t.Errorf("heartbeat on lapsed lease err = %v, want ErrLockHeld", err)
	}

	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if b.Kind != store.AcquireGranted {
		t.Fatalf("B kind = %q, want granted (A's lapsed lease should be reclaimed)", b.Kind)
	}
}

func TestS3Concurrency_FallsBackWhenPreconditionsIgnored(t *testing.T) {
	c := orchestrator.NewS3Concurrency(&ignorePreconditionsStore{})
	key := "g:fallback"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A kind = %q, want granted", a.Kind)
	}
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if b.Kind != store.AcquireGranted {
		t.Errorf("B kind = %q, want granted (no-op fallback grants every slot)", b.Kind)
	}
}

func TestS3Concurrency_NonConditionalStoreIsNoop(t *testing.T) {
	c := orchestrator.NewS3Concurrency(&plainStore{})
	key := "g:plain"

	a := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "A", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	b := acquire(t, c, store.AcquireSlotRequest{Key: key, RunID: "B", NodeID: "n", Capacity: 1, Policy: store.OnLimitQueue})
	if a.Kind != store.AcquireGranted || b.Kind != store.AcquireGranted {
		t.Errorf("kinds = %q,%q, want granted,granted (non-conditional store is no-op)", a.Kind, b.Kind)
	}
}

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
