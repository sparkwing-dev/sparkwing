package logbatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
)

// safety: one Append is one object here, which is the cost the
// batcher exists to reduce.
type countingStore struct {
	mu      sync.Mutex
	objects map[string][][]byte
	appends int
	failNow error
}

func newCountingStore() *countingStore {
	return &countingStore{objects: map[string][][]byte{}}
}

func key(runID, nodeID string) string { return runID + "/" + nodeID }

func (c *countingStore) Append(_ context.Context, runID, nodeID string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failNow != nil {
		return c.failNow
	}
	c.appends++
	c.objects[key(runID, nodeID)] = append(c.objects[key(runID, nodeID)], bytes.Clone(data))
	return nil
}

func (c *countingStore) Read(_ context.Context, runID, nodeID string, _ storage.ReadOpts) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	for _, o := range c.objects[key(runID, nodeID)] {
		buf.Write(o)
	}
	return buf.Bytes(), nil
}

func (c *countingStore) ReadRun(_ context.Context, runID string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	for k, objs := range c.objects {
		if !strings.HasPrefix(k, runID+"/") {
			continue
		}
		for _, o := range objs {
			buf.Write(o)
		}
	}
	return buf.Bytes(), nil
}

func (c *countingStore) Stream(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func (c *countingStore) DeleteRun(_ context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.objects {
		if strings.HasPrefix(k, runID+"/") {
			delete(c.objects, k)
		}
	}
	return nil
}

func (c *countingStore) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appends
}

func (c *countingStore) objectsFor(runID, nodeID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.objects[key(runID, nodeID)])
}

func (c *countingStore) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failNow = err
}

var _ storage.LogStore = (*countingStore)(nil)

func line(n int) []byte { return []byte(fmt.Sprintf(`{"msg":"line %d"}`+"\n", n)) }

func TestFlushesOnSizeThreshold(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate, logbatch.WithBufferThreshold(64), logbatch.WithFlushInterval(time.Hour))
	defer closeStore(t, s)

	ctx := t.Context()
	for i := range 10 {
		if err := s.Append(ctx, "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := delegate.count(); got == 0 || got >= 10 {
		t.Fatalf("size-triggered flushes = %d, want between 1 and 9", got)
	}
}

func TestFlushesOnInterval(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(10*time.Millisecond))
	defer closeStore(t, s)

	if err := s.Append(t.Context(), "run-1", "build", line(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	waitFor(t, time.Second, func() bool { return delegate.count() == 1 })
}

func TestFlushNodeWritesOneObject(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(time.Hour))
	defer closeStore(t, s)

	ctx := t.Context()
	for i := range 100 {
		if err := s.Append(ctx, "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := delegate.count(); got != 0 {
		t.Fatalf("appends before finish = %d, want 0", got)
	}
	if err := s.FlushNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("FlushNode: %v", err)
	}
	if got := delegate.objectsFor("run-1", "build"); got != 1 {
		t.Fatalf("objects after finish = %d, want 1", got)
	}
	body, err := delegate.Read(ctx, "run-1", "build", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n := bytes.Count(body, []byte("\n")); n != 100 {
		t.Fatalf("lines in the flushed object = %d, want 100", n)
	}
}

func TestCloseFlushesWhatIsBuffered(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(time.Hour))
	if err := s.Append(t.Context(), "run-1", "build", line(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := delegate.count(); got != 1 {
		t.Fatalf("objects after Close = %d, want 1", got)
	}
	if err := s.Append(t.Context(), "run-1", "build", line(2)); !errors.Is(err, logbatch.ErrClosed) {
		t.Fatalf("Append after Close = %v, want ErrClosed", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestObjectCapDropsLinesAndMarksThem(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	// safety: a one-byte threshold makes every append its own object, so
	// the cap lands after a countable number of them.
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1),
		logbatch.WithFlushInterval(time.Hour),
		logbatch.WithMaxObjects(3))
	defer closeStore(t, s)

	ctx := t.Context()
	for i := range 20 {
		if err := s.Append(ctx, "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := delegate.objectsFor("run-1", "build"); got != 3 {
		t.Fatalf("objects at the cap = %d, want 3", got)
	}
	if err := s.FlushNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("FlushNode: %v", err)
	}
	if got := delegate.objectsFor("run-1", "build"); got != 4 {
		t.Fatalf("objects including the marker = %d, want 4", got)
	}
	body, err := delegate.Read(ctx, "run-1", "build", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := logbatch.DropMarkerPrefix + "17 "
	if !bytes.Contains(body, []byte(want)) {
		t.Fatalf("marker line missing %q from:\n%s", want, body)
	}
}

func TestByteCapDropsLines(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(time.Hour),
		logbatch.WithMaxBytes(int64(len(line(0)))*4))
	defer closeStore(t, s)

	ctx := t.Context()
	for i := range 10 {
		if err := s.Append(ctx, "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := s.FlushNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("FlushNode: %v", err)
	}
	body, err := delegate.Read(ctx, "run-1", "build", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n := bytes.Count(body, []byte(`"line `)); n != 4 {
		t.Fatalf("lines kept under the byte cap = %d, want 4", n)
	}
	if !bytes.Contains(body, []byte(logbatch.DropMarkerPrefix+"6 ")) {
		t.Fatalf("marker for 6 dropped lines missing from:\n%s", body)
	}
}

func TestReadFlushesPendingLines(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(time.Hour))
	defer closeStore(t, s)

	ctx := t.Context()
	if err := s.Append(ctx, "run-1", "build", line(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	body, err := s.Read(ctx, "run-1", "build", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Contains(body, []byte(`"line 1"`)) {
		t.Fatalf("Read did not flush the buffer, got %q", body)
	}

	if err := s.Append(ctx, "run-1", "build", line(2)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	run, err := s.ReadRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if !bytes.Contains(run, []byte(`"line 2"`)) {
		t.Fatalf("ReadRun did not flush the buffer, got %q", run)
	}
}

func TestDeleteRunDiscardsBuffers(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1<<20),
		logbatch.WithFlushInterval(time.Hour))
	defer closeStore(t, s)

	ctx := t.Context()
	if err := s.Append(ctx, "run-1", "build", line(1)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.DeleteRun(ctx, "run-1"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if err := s.FlushNode(ctx, "run-1", "build"); err != nil {
		t.Fatalf("FlushNode: %v", err)
	}
	if got := delegate.count(); got != 0 {
		t.Fatalf("objects after DeleteRun = %d, want 0", got)
	}
}

func TestFlushErrorSurfacesOnTheNextAppend(t *testing.T) {
	t.Parallel()
	delegate := newCountingStore()
	s := logbatch.New(delegate,
		logbatch.WithBufferThreshold(1),
		logbatch.WithFlushInterval(time.Hour))
	defer closeStore(t, s)

	ctx := t.Context()
	boom := errors.New("bucket unreachable")
	delegate.fail(boom)
	if err := s.Append(ctx, "run-1", "build", line(1)); !errors.Is(err, boom) {
		t.Fatalf("Append = %v, want the flush error", err)
	}
	delegate.fail(nil)
	if err := s.Append(ctx, "run-1", "build", line(2)); err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
}

// TestObjectCountForFiveThousandLines pins the reason the package
// exists: an unbatched object store writes one object per line, the
// batcher writes one per flush.
func TestObjectCountForFiveThousandLines(t *testing.T) {
	t.Parallel()
	const lines = 5000

	unbatched := newCountingStore()
	for i := range lines {
		if err := unbatched.Append(t.Context(), "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if got := unbatched.objectsFor("run-1", "build"); got != lines {
		t.Fatalf("unbatched objects = %d, want %d", got, lines)
	}

	batchedDelegate := newCountingStore()
	s := logbatch.New(batchedDelegate, logbatch.WithFlushInterval(time.Hour))
	for i := range lines {
		if err := s.Append(t.Context(), "run-1", "build", line(i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.FlushNode(t.Context(), "run-1", "build"); err != nil {
		t.Fatalf("FlushNode: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := batchedDelegate.objectsFor("run-1", "build")
	if got > 3 {
		t.Fatalf("batched objects = %d, want at most 3 at the 256 KiB threshold", got)
	}
	t.Logf("5000 lines: unbatched %d objects, batched %d objects", lines, got)
}

func closeStore(t *testing.T, s *logbatch.Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
