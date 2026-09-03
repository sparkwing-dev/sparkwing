package orchestrator_test

import (
	"context"
	"errors"
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
