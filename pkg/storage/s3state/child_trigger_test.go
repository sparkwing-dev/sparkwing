package s3state_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/s3state"
)

type failWritePrefixArt struct {
	*memCondArt
	mu     sync.Mutex
	prefix string
	err    error
}

func (a *failWritePrefixArt) PutIfAbsent(ctx context.Context, key string, r io.Reader) (storage.ETag, error) {
	a.mu.Lock()
	prefix, err := a.prefix, a.err
	a.mu.Unlock()
	if err != nil && strings.HasPrefix(key, prefix) {
		return "", err
	}
	return a.memCondArt.PutIfAbsent(ctx, key, r)
}

func (a *failWritePrefixArt) recover() {
	a.mu.Lock()
	a.err = nil
	a.mu.Unlock()
}

func TestS3CAS_EnqueueTrigger_RetryAfterAFailedRecordWriteStillSpawns(t *testing.T) {
	art := &failWritePrefixArt{
		memCondArt: newMemCondArt(),
		prefix:     "triggers/by-id/",
		err:        errors.New("s3: 500 internal error"),
	}
	b := s3state.New(art)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	if _, err := b.EnqueueTrigger(ctx, "deploy", nil, "parent-run", "node-1", "", "await-pipeline", "", "", ""); err == nil {
		t.Fatal("EnqueueTrigger reported success though the trigger record write failed")
	}

	art.recover()
	id, err := b.EnqueueTrigger(ctx, "deploy", nil, "parent-run", "node-1", "", "await-pipeline", "", "", "")
	if err != nil {
		t.Fatalf("EnqueueTrigger retry: %v", err)
	}
	if _, err := b.GetTrigger(ctx, id); err != nil {
		t.Fatalf("the retry returned trigger id %q, which does not resolve: %v", id, err)
	}
	found, err := b.FindSpawnedChildTriggerID(ctx, "parent-run", "node-1", "deploy")
	if err != nil {
		t.Fatalf("FindSpawnedChildTriggerID: %v", err)
	}
	if found != id {
		t.Errorf("child index points at %q, want the retry's trigger %q", found, id)
	}
}
