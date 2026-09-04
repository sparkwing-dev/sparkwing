package s3state

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type triggerCASArt struct {
	*stubArt
	rejectTriggerWrites int
	triggerWrites       int
}

func newTriggerCASArt() *triggerCASArt {
	return &triggerCASArt{stubArt: newStubArt()}
}

func (a *triggerCASArt) GetWithETag(ctx context.Context, key string) (io.ReadCloser, storage.ETag, error) {
	rc, err := a.Get(ctx, key)
	return rc, "etag", err
}

func (a *triggerCASArt) PutIfAbsent(_ context.Context, key string, r io.Reader) (storage.ETag, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.HasPrefix(key, "triggers/by-id/") {
		a.triggerWrites++
		if a.rejectTriggerWrites > 0 {
			a.rejectTriggerWrites--
			return "", storage.ErrPreconditionFailed
		}
	}
	if _, exists := a.data[key]; exists {
		return "", storage.ErrPreconditionFailed
	}
	a.data[key] = bytes.Clone(body)
	return "etag", nil
}

func (a *triggerCASArt) PutIfMatch(ctx context.Context, key string, r io.Reader, _ storage.ETag) (storage.ETag, error) {
	if err := a.Put(ctx, key, r); err != nil {
		return "", err
	}
	return "etag", nil
}

func (a *triggerCASArt) ConditionalWritesSupported(context.Context) (bool, error) {
	return true, nil
}

func TestTriggerRunIDUses128BitsOfEntropy(t *testing.T) {
	id, err := triggerRunID()
	if err != nil {
		t.Fatalf("triggerRunID: %v", err)
	}
	suffix := id[strings.LastIndexByte(id, '-')+1:]
	raw, err := hex.DecodeString(suffix)
	if err != nil {
		t.Fatalf("trigger id suffix %q is not hexadecimal: %v", suffix, err)
	}
	if len(raw) != 16 {
		t.Fatalf("trigger id entropy = %d bytes, want 16", len(raw))
	}
}

func TestEnqueueTriggerPropagatesInitialMintFailure(t *testing.T) {
	wantErr := errors.New("entropy unavailable")
	art := newTriggerCASArt()
	b := New(art)
	t.Cleanup(func() { _ = b.Close() })
	b.triggerIDMinter = func() (string, error) { return "", wantErr }

	if _, err := b.EnqueueTrigger(context.Background(), "deploy", nil, "", "", "", "", "", "", ""); !errors.Is(err, wantErr) {
		t.Fatalf("EnqueueTrigger error = %v, want entropy failure", err)
	}
	if art.triggerWrites != 0 {
		t.Fatalf("trigger record writes = %d, want 0 after mint failure", art.triggerWrites)
	}
}

func TestEnqueueTriggerPropagatesRemintFailure(t *testing.T) {
	wantErr := errors.New("entropy unavailable")
	art := newTriggerCASArt()
	art.rejectTriggerWrites = 1
	b := New(art)
	t.Cleanup(func() { _ = b.Close() })
	mints := 0
	b.triggerIDMinter = func() (string, error) {
		mints++
		if mints == 1 {
			return "run-first", nil
		}
		return "", wantErr
	}

	if _, err := b.EnqueueTrigger(context.Background(), "deploy", nil, "", "", "", "", "", "", ""); !errors.Is(err, wantErr) {
		t.Fatalf("EnqueueTrigger error = %v, want remint entropy failure", err)
	}
	if mints != 2 {
		t.Fatalf("id mint calls = %d, want 2", mints)
	}
	if art.triggerWrites != 1 {
		t.Fatalf("trigger record writes = %d, want 1 before remint failure", art.triggerWrites)
	}
}

func TestEnqueueTriggerCapsCollisionRemints(t *testing.T) {
	art := newTriggerCASArt()
	art.rejectTriggerWrites = maxTriggerIDAttempts
	b := New(art)
	t.Cleanup(func() { _ = b.Close() })
	mints := 0
	b.triggerIDMinter = func() (string, error) {
		mints++
		return fmt.Sprintf("run-%d", mints), nil
	}

	if _, err := b.EnqueueTrigger(context.Background(), "deploy", nil, "", "", "", "", "", "", ""); !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("EnqueueTrigger error = %v, want collision exhaustion", err)
	}
	if mints != maxTriggerIDAttempts {
		t.Fatalf("id mint calls = %d, want %d", mints, maxTriggerIDAttempts)
	}
	if art.triggerWrites != maxTriggerIDAttempts {
		t.Fatalf("trigger record writes = %d, want %d", art.triggerWrites, maxTriggerIDAttempts)
	}
}
