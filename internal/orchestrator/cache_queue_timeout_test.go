package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// queueTimeoutLeaderPipe holds the gate long enough that a follower
// with a short QueueTimeout gives up before the slot frees.
type queueTimeoutLeaderPipe struct{ sparkwing.Base }

func (queueTimeoutLeaderPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "leader", cacheStep(2*time.Second)).
		Cache(sparkwing.CacheOptions{Namespace: "cache-queue-timeout-key"})
	return nil
}

type queueTimeoutFollowerPipe struct{ sparkwing.Base }

func (queueTimeoutFollowerPipe) Plan(ctx context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, "follower", cacheStep(50*time.Millisecond)).
		Cache(sparkwing.CacheOptions{
			Namespace:    "cache-queue-timeout-key",
			OnLimit:      sparkwing.Queue,
			QueueTimeout: 200 * time.Millisecond,
		})
	return nil
}

func init() {
	register("cache-queue-timeout-leader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &queueTimeoutLeaderPipe{} })
	register("cache-queue-timeout-follower", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &queueTimeoutFollowerPipe{} })
}

// TestCache_QueueTimeoutFailsWaiterCleanly: a queued follower whose
// QueueTimeout elapses before the leader releases must fail with
// failure_reason "queue_timeout" -- not wait forever, and not get
// promoted later once the leader frees the slot.
func TestCache_QueueTimeoutFailsWaiterCleanly(t *testing.T) {
	resetCacheCounter()
	p := newPaths(t)

	leaderDone := make(chan *orchestrator.Result, 1)
	go func() {
		res, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-queue-timeout-leader"})
		leaderDone <- res
	}()
	time.Sleep(150 * time.Millisecond) // leader acquires the slot

	followerRes, _ := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "cache-queue-timeout-follower"})
	if followerRes.Status != "failed" {
		t.Fatalf("follower status = %q, want failed (QueueTimeout elapsed)", followerRes.Status)
	}

	st, _ := store.Open(p.StateDB())
	defer func() { _ = st.Close() }()
	nodes, _ := st.ListNodes(context.Background(), followerRes.RunID)
	if len(nodes) != 1 {
		t.Fatalf("follower run: expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Outcome != string(sparkwing.Failed) {
		t.Fatalf("follower node outcome = %q, want failed", nodes[0].Outcome)
	}
	if nodes[0].FailureReason != store.FailureQueueTimeout {
		t.Fatalf("follower failure_reason = %q, want %q", nodes[0].FailureReason, store.FailureQueueTimeout)
	}
	if !strings.Contains(nodes[0].Error, "OnLimit:Queue") {
		t.Fatalf("follower error = %q, want a message mentioning OnLimit:Queue", nodes[0].Error)
	}

	// The follower never ran its body (it timed out queued).
	leaderRes := <-leaderDone
	if leaderRes.Status != "success" {
		t.Fatalf("leader status = %q, want success", leaderRes.Status)
	}
	if peak := cacheCounter.max.Load(); peak > 1 {
		t.Fatalf("peak concurrency = %d, want 1 (follower must not have run)", peak)
	}
}
