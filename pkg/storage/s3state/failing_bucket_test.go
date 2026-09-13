package s3state

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var errBucketDown = errors.New("fake bucket: permanently unavailable")

type failingBucket struct {
	mu                   sync.Mutex
	puts, gets, lists    int
	preconditionInstead  bool
	conditionalSupported bool
	writable             bool
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

func (f *failingBucket) accept() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writable = true
}

func (f *failingBucket) Put(_ context.Context, _ string, r io.Reader) error {
	f.mu.Lock()
	f.puts++
	writable := f.writable
	f.mu.Unlock()
	_, _ = io.Copy(io.Discard, r)
	if writable {
		return nil
	}
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

// TestOutboxReplayPacesItselfAfterGivingUp pins the replay bound:
// background replay spends its attempt cap, reports the stall, and then
// retries at its ceiling rather than re-billing a PUT every interval.
func TestOutboxReplayPacesItselfAfterGivingUp(t *testing.T) {
	const (
		maxAttempts = 6
		maxBackoff  = 150 * time.Millisecond
	)
	bucket := &failingBucket{}
	path := filepath.Join(t.TempDir(), "outbox.db")
	ob, err := openOutboxWithReplayPolicy(path, bucket, time.Millisecond, slog.Default(), maxAttempts, maxBackoff)
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
	if !strings.Contains(stalled.Error(), "gave up") {
		t.Errorf("the stall error does not say the drainer gave up: %v", stalled)
	}
	found := false
	for _, st := range objectguard.Stalls() {
		if strings.Contains(st.Path, path) {
			found = true
		}
	}
	if !found {
		t.Fatal("a stalled replay is invisible to the health route; it is not in objectguard.Stalls")
	}

	// safety: the drainer keeps its place at the backoff ceiling rather than
	// stopping, so a bucket that returns hours later still delivers.
	time.Sleep(300 * time.Millisecond)
	after, _, _ := bucket.counts()
	if after > maxAttempts+3 {
		t.Fatalf("replay sent %d puts after giving up, want it paced at its ceiling", after)
	}
}

// TestOutboxReplayResumesAndClearsItsStall proves the stall is a state,
// not a terminus: a bucket that comes back drains the queue and the
// health route stops reporting it.
func TestOutboxReplayResumesAndClearsItsStall(t *testing.T) {
	const maxAttempts = 3
	bucket := &failingBucket{}
	path := filepath.Join(t.TempDir(), "outbox.db")
	ob, err := openOutboxWithReplayPolicy(path, bucket, time.Millisecond, slog.Default(), maxAttempts, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer func() { _ = ob.Close() }()

	ctx := context.Background()
	if err := ob.Stage(ctx, OutboxKindState, "runs/r1/state.ndjson", []byte("{}\n")); err != nil {
		t.Fatalf("stage: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ob.ReplayStalled() == nil {
		time.Sleep(2 * time.Millisecond)
	}
	if ob.ReplayStalled() == nil {
		t.Fatal("background replay never gave up")
	}

	bucket.accept()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ob.ReplayStalled() != nil {
		time.Sleep(2 * time.Millisecond)
	}
	if err := ob.ReplayStalled(); err != nil {
		t.Fatalf("the drainer stayed stalled after the bucket came back: %v", err)
	}
	if n, perr := ob.Pending(ctx); perr != nil || n != 0 {
		t.Fatalf("the queue holds %d row(s) after the bucket came back (err %v)", n, perr)
	}
	for _, st := range objectguard.Stalls() {
		if strings.Contains(st.Path, path) {
			t.Fatal("a recovered replay still reports a stall to the health route")
		}
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
	bucket := &selectiveBucket{failing: map[string]bool{"runs/run-failing/state.ndjson": true}}
	b := New(bucket, WithFlushInterval(interval))
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	for _, id := range []string{"run-failing", "run-healthy"} {
		if err := b.CreateRun(ctx, store.Run{ID: id, Pipeline: "p"}); err != nil && !errors.Is(err, errBucketDown) {
			t.Fatalf("create run %s: %v", id, err)
		}
	}
	time.Sleep(window)

	puts := bucket.putsTo("runs/run-failing/state.ndjson")
	if puts == 0 {
		t.Fatal("the flush loop never tried to write, so the bound proves nothing")
	}
	if puts > requestBound {
		t.Fatalf("the flush loop sent %d puts in %s against a failing bucket, want at most %d", puts, window, requestBound)
	}
	unbounded := int(window / interval)
	if puts >= unbounded {
		t.Fatalf("the flush loop sent %d puts, the unpaced rate of %d per %s; a healthy run beside a failing one must not reset the backoff",
			puts, unbounded, window)
	}
}

type selectiveBucket struct {
	mu      sync.Mutex
	failing map[string]bool
	puts    map[string]int
	data    map[string][]byte
}

func (s *selectiveBucket) putsTo(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts[key]
}

func (s *selectiveBucket) Put(_ context.Context, key string, r io.Reader) error {
	body, _ := io.ReadAll(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.puts == nil {
		s.puts = map[string]int{}
	}
	s.puts[key]++
	if s.failing[key] {
		return errBucketDown
	}
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[key] = body
	return nil
}

func (s *selectiveBucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (s *selectiveBucket) Has(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[key]
	return ok, nil
}

func (s *selectiveBucket) Delete(context.Context, string) error { return nil }

func (s *selectiveBucket) List(context.Context, string) ([]string, error) {
	return nil, storage.ErrListNotSupported
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
