package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type flakyProbeStore struct {
	storage.ArtifactStore
	storage.ConditionalWriter
	probes atomic.Int32
}

func (s *flakyProbeStore) ConditionalWritesSupported(ctx context.Context) (bool, error) {
	if s.probes.Add(1) == 1 {
		return false, errors.New("dial s3: connection reset by peer")
	}
	return s.ConditionalWriter.ConditionalWritesSupported(ctx)
}

func TestS3Concurrency_TransientProbeErrorLeavesReservationIntact(t *testing.T) {
	art, _ := openIntegrationS3(t)
	cw, ok := storage.Conditional(art)
	if !ok {
		t.Fatal("integration S3 store does not implement ConditionalWriter")
	}
	c := orchestrator.NewS3Concurrency(&flakyProbeStore{ArtifactStore: art, ConditionalWriter: cw})
	key := "g:probe-blip"
	req := func(runID string) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: key, RunID: runID, NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Minute,
		}
	}

	if _, err := c.AcquireSlot(context.Background(), req("A")); err == nil {
		t.Fatal("acquire over a failed probe returned no error; a failed probe is not an answer about the store")
	}

	a := acquire(t, c, req("A"))
	if a.Kind != store.AcquireGranted {
		t.Fatalf("A = %s, want Granted", a.Kind)
	}
	b := acquire(t, c, req("B"))
	if b.Kind != store.AcquireQueued {
		t.Fatalf("B = %s, want Queued; one failed probe left the process with no reservation", b.Kind)
	}
}

type blockingProbeStore struct {
	storage.ArtifactStore
	storage.ConditionalWriter
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	probes  atomic.Int32
}

func (s *blockingProbeStore) ConditionalWritesSupported(ctx context.Context) (bool, error) {
	s.probes.Add(1)
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.ConditionalWriter.ConditionalWritesSupported(ctx)
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func TestS3Concurrency_ProbeIsSharedAndDoesNotParkOtherCallers(t *testing.T) {
	art, _ := openIntegrationS3(t)
	cw, ok := storage.Conditional(art)
	if !ok {
		t.Fatal("integration S3 store does not implement ConditionalWriter")
	}
	store3 := &blockingProbeStore{
		ArtifactStore:     art,
		ConditionalWriter: cw,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	c := orchestrator.NewS3Concurrency(store3)
	req := func(runID string) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: "g:probe-shared", RunID: runID, NodeID: "n",
			Capacity: 1, Policy: store.OnLimitQueue, Lease: time.Minute,
		}
	}

	first := make(chan error, 1)
	go func() {
		_, err := c.AcquireSlot(context.Background(), req("A"))
		first <- err
	}()
	<-store3.entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.AcquireSlot(ctx, req("B"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire while a probe is in flight: err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("acquire returned after %s, want near its 100ms deadline", elapsed)
	}

	close(store3.release)
	if err := <-first; err != nil {
		t.Fatalf("acquire that ran the probe: %v", err)
	}
	if n := store3.probes.Load(); n != 1 {
		t.Fatalf("store was probed %d times, want 1 shared probe", n)
	}
}

func TestS3Concurrency_ProbeOutlivesTheCallerThatStartedIt(t *testing.T) {
	art, _ := openIntegrationS3(t)
	cw, ok := storage.Conditional(art)
	if !ok {
		t.Fatal("integration S3 store does not implement ConditionalWriter")
	}
	blocking := &blockingProbeStore{
		ArtifactStore:     art,
		ConditionalWriter: cw,
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
	c := orchestrator.NewS3Concurrency(blocking)
	req := func(runID string) store.AcquireSlotRequest {
		return store.AcquireSlotRequest{
			Key: "g:probe-outlives", RunID: runID, NodeID: "n",
			Capacity: 2, Policy: store.OnLimitQueue, Lease: time.Minute,
		}
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderErr := make(chan error, 1)
	go func() {
		_, err := c.AcquireSlot(leaderCtx, req("A"))
		leaderErr <- err
	}()
	<-blocking.entered
	cancelLeader()
	if err := <-leaderErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire whose context was cancelled mid-probe: err = %v, want context.Canceled", err)
	}

	type acquired struct {
		resp store.AcquireSlotResponse
		err  error
	}
	follower := make(chan acquired, 1)
	go func() {
		resp, err := c.AcquireSlot(context.Background(), req("B"))
		follower <- acquired{resp, err}
	}()
	close(blocking.release)

	got := <-follower
	if got.err != nil {
		t.Fatalf("acquire after the probe's starter was cancelled: %v", got.err)
	}
	if got.resp.Kind != store.AcquireGranted {
		t.Fatalf("follower = %s, want Granted from a real reservation", got.resp.Kind)
	}
	if n := blocking.probes.Load(); n != 1 {
		t.Fatalf("store was probed %d times, want the one probe the cancelled caller started", n)
	}
}
