package s3state

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var errBucketDown = errors.New("fake bucket: permanently unavailable")

type failingBucket struct {
	mu                   sync.Mutex
	puts, gets, lists    int
	preconditionInstead  bool
	conditionalSupported bool
}

func (f *failingBucket) counts() (puts, gets, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts, f.gets, f.lists
}

func (f *failingBucket) total() int {
	puts, gets, lists := f.counts()
	return puts + gets + lists
}

func (f *failingBucket) Get(context.Context, string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.gets++
	f.mu.Unlock()
	return nil, storage.ErrNotFound
}

func (f *failingBucket) Put(_ context.Context, _ string, r io.Reader) error {
	f.mu.Lock()
	f.puts++
	f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r)
	return errBucketDown
}

func (f *failingBucket) Has(context.Context, string) (bool, error) {
	f.mu.Lock()
	f.gets++
	f.mu.Unlock()
	return false, nil
}

func (f *failingBucket) Delete(context.Context, string) error { return nil }

func (f *failingBucket) List(context.Context, string) ([]string, error) {
	f.mu.Lock()
	f.lists++
	f.mu.Unlock()
	return nil, nil
}

func (f *failingBucket) GetWithETag(context.Context, string) (io.ReadCloser, storage.ETag, error) {
	f.mu.Lock()
	f.gets++
	f.mu.Unlock()
	return nil, "", storage.ErrNotFound
}

func (f *failingBucket) PutIfAbsent(_ context.Context, _ string, r io.Reader) (storage.ETag, error) {
	f.mu.Lock()
	f.puts++
	f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r)
	if f.preconditionInstead {
		return "", storage.ErrPreconditionFailed
	}
	return "", errBucketDown
}

func (f *failingBucket) PutIfMatch(_ context.Context, _ string, r io.Reader, _ storage.ETag) (storage.ETag, error) {
	f.mu.Lock()
	f.puts++
	f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r)
	if f.preconditionInstead {
		return "", storage.ErrPreconditionFailed
	}
	return "", errBucketDown
}

func (f *failingBucket) ConditionalWritesSupported(context.Context) (bool, error) {
	return f.conditionalSupported, nil
}

// TestOutboxReplayStopsAgainstAPermanentlyFailingBucket pins the replay
// bound: whatever the bucket does, background replay spends its attempt
// cap and reports, rather than re-billing a PUT every interval forever.
func TestOutboxReplayStopsAgainstAPermanentlyFailingBucket(t *testing.T) {
	const maxAttempts = 6
	bucket := &failingBucket{}
	path := filepath.Join(t.TempDir(), "outbox.db")
	ob, err := openOutboxWithReplayPolicy(path, bucket, time.Millisecond, slog.Default(), maxAttempts, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer func() { _ = ob.Close() }()

	if err := ob.Stage(context.Background(), OutboxKindState, "runs/r1/state.ndjson", []byte("{}\n")); err != nil {
		t.Fatalf("stage: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ob.ReplayStalled() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	stalled := ob.ReplayStalled()
	if stalled == nil {
		t.Fatal("background replay never gave up on a bucket that refuses every write")
	}
	puts, _, _ := bucket.counts()
	if puts != maxAttempts {
		t.Fatalf("replay sent %d puts, want its cap of %d", puts, maxAttempts)
	}

	time.Sleep(200 * time.Millisecond)
	if after, _, _ := bucket.counts(); after != maxAttempts {
		t.Fatalf("replay kept going after giving up: %d puts, want %d", after, maxAttempts)
	}
}

// TestFlushLoopBacksOffAgainstAPermanentlyFailingBucket states the
// bound the ticket asks for: over a fixed wall-clock window the flush
// loop reaches an unreachable store a handful of times, not once per
// interval.
func TestFlushLoopBacksOffAgainstAPermanentlyFailingBucket(t *testing.T) {
	const (
		window       = time.Second
		interval     = 5 * time.Millisecond
		requestBound = 25
	)
	bucket := &failingBucket{}
	b := New(bucket, WithFlushInterval(interval))
	defer func() { _ = b.Close() }()

	if err := b.CreateRun(context.Background(), store.Run{ID: "run-failing", Pipeline: "p"}); err != nil && !errors.Is(err, errBucketDown) {
		t.Fatalf("create run: %v", err)
	}
	time.Sleep(window)

	puts, _, _ := bucket.counts()
	if puts == 0 {
		t.Fatal("the flush loop never tried to write, so the bound proves nothing")
	}
	if puts > requestBound {
		t.Fatalf("the flush loop sent %d puts in %s against a failing bucket, want at most %d", puts, window, requestBound)
	}
	unbounded := int(window / interval)
	if puts >= unbounded {
		t.Fatalf("the flush loop sent %d puts, which is the unpaced rate of %d per %s", puts, unbounded, window)
	}
}

// TestCASLoopStaysBoundedUnderPermanentContention covers the
// conditional-write retry loops: a store that never lets a write win
// costs a bounded number of requests over a fixed window instead of
// spinning at wire speed.
func TestCASLoopStaysBoundedUnderPermanentContention(t *testing.T) {
	const (
		window       = 750 * time.Millisecond
		requestBound = 200
	)
	bucket := &failingBucket{preconditionInstead: true, conditionalSupported: true}
	b := New(bucket, WithFlushInterval(time.Hour))
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	calls := 0
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		err := b.CreateApproval(ctx, store.Approval{RunID: "run-1", NodeID: "node-1"})
		if err == nil {
			t.Fatal("a store that refuses every conditional write reported a successful approval")
		}
		calls++
	}
	if calls == 0 {
		t.Fatal("no CAS call completed inside the window")
	}
	if total := bucket.total(); total > requestBound {
		t.Fatalf("%d object-store requests in %s across %d exhausted CAS calls, want at most %d", total, window, calls, requestBound)
	}
}
