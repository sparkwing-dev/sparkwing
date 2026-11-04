package logseg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
)

const testPrefix = "logs/r90/teams/acme/runs/run-1/jobs/build/a1-g1/"

type manualTimer struct {
	mu sync.Mutex
	f  func()
}

func (m *manualTimer) after(_ time.Duration, f func()) func() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.f = f
	return func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.f = nil
		return true
	}
}

func (m *manualTimer) fire() bool {
	m.mu.Lock()
	f := m.f
	m.f = nil
	m.mu.Unlock()
	if f == nil {
		return false
	}
	f()
	return true
}

func newSink(t *testing.T, up Uploader, opts Options) *Sink {
	t.Helper()
	opts.SpoolDir = t.TempDir()
	if opts.Subject.Kind == "" {
		opts.Subject = keySubject("acme", "run-1", "build", 1, 1)
	}
	if opts.AfterFunc == nil {
		opts.AfterFunc = (&manualTimer{}).after
	}
	opts.RetryBase, opts.RetryMax = time.Millisecond, time.Millisecond
	s, err := NewSink(t.Context(), up, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func line(i int) string { return fmt.Sprintf(`{"msg":"line %03d"}`+"\n", i) }

func readLog(t *testing.T, objs *memObjects, opts storage.ReadOpts) (ReadResult, []byte, error) {
	t.Helper()
	var out bytes.Buffer
	reader := CommittedReader{Records: objs, Objects: objs}
	result, err := reader.Read(t.Context(), runstate.Actor{}, blob.LogReadRequest{Subject: objs.view.Subject}, &out)
	if err != nil {
		return result, out.Bytes(), err
	}
	body, err := opts.Apply(out.Bytes())
	return result, body, err
}

func readAll(t *testing.T, objs *memObjects, opts storage.ReadOpts) (ReadResult, string) {
	t.Helper()
	result, body, err := readLog(t, objs, opts)
	if err != nil {
		t.Fatal(err)
	}
	return result, string(body)
}

func waitStored(t *testing.T, up *memUploader, n int) {
	t.Helper()
	for got := range up.stored {
		if got >= n {
			return
		}
	}
}

func TestSinkCutsBySizeAndSealsComplete(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	s := newSink(t, up, Options{SegmentBytes: 64})
	var want strings.Builder
	for i := 1; i <= 20; i++ {
		if err := s.Append([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
		want.WriteString(line(i))
	}
	m, err := s.Close(context.Background(), End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != StatusComplete || m.Lines != 20 || len(m.Segments) < 2 {
		t.Fatalf("manifest = %+v", m)
	}
	l, got := readAll(t, objs, storage.ReadOpts{})
	if got != want.String() {
		t.Fatalf("read %q, want %q", got, want.String())
	}
	if !l.Complete || l.Bytes != m.Bytes {
		t.Fatalf("committed read = %+v", l)
	}
}

func TestSinkCutsOnIntervalOnlyWithBytesPending(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	clock := &manualTimer{}
	s := newSink(t, up, Options{AfterFunc: clock.after})
	if clock.fire() {
		t.Fatal("an idle sink armed the interval cut")
	}
	if err := s.Append([]byte(line(1))); err != nil {
		t.Fatal(err)
	}
	if !clock.fire() {
		t.Fatal("pending bytes armed no interval cut")
	}
	waitStored(t, up, 1)
	if clock.fire() {
		t.Fatal("the interval cut rearmed with nothing pending")
	}
	if _, err := s.Close(context.Background(), End{}); err != nil {
		t.Fatal(err)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1) {
		t.Fatalf("read %q", got)
	}
}

func TestKilledRunnerRetainsOnlyCommittedSegments(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	s := newSink(t, up, Options{SegmentBytes: 1})
	if err := s.Append([]byte(line(1))); err != nil {
		t.Fatal(err)
	}
	waitStored(t, up, 1)
	up.kill()
	if err := s.Append([]byte(line(2))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(t.Context(), End{}); err == nil {
		t.Fatal("dead runner sealed")
	}
	result, got := readAll(t, objs, storage.ReadOpts{})
	if result.Complete || got != line(1) {
		t.Fatalf("read=%+v, %q", result, got)
	}
}

func TestSinkStopsAtTheByteCap(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), Options{MaxBytes: 30})
	for i := 1; i <= 4; i++ {
		if err := s.Append([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.Close(context.Background(), End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != statusCutOff || m.Reason != reasonByteCap || m.DroppedLines != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1)+truncationMarker {
		t.Fatalf("read %q", got)
	}
}

func TestSinkReportsFailedUploads(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	up.failSeg = func(n int) bool { return n == 2 }
	s := newSink(t, up, Options{SegmentBytes: int64(len(line(0)))})
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.Close(context.Background(), End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != statusIncomplete || m.Reason != reasonUploadFailed || len(m.Segments) != 1 || m.DroppedLines != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1) {
		t.Fatalf("read %q", got)
	}
}

func TestReaderStopsAtASegmentFailingItsDigest(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), Options{SegmentBytes: int64(len(line(0)))})
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(context.Background(), End{}); err != nil {
		t.Fatal(err)
	}
	objs.put(blob.LogSegmentKey(testPrefix, 2), []byte("tampered"))
	result, got, err := readLog(t, objs, storage.ReadOpts{})
	if !errors.Is(err, blob.ErrIntegrity) || string(got) != line(1) || result.Complete {
		t.Fatalf("read=%+v, %q, %v", result, got, err)
	}
}

func TestSegmentReadsApplyFiltersAcrossCuts(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), Options{SegmentBytes: 1})
	for _, body := range []string{"alpha", "keep one\n", "beta\n", "keep two", "gamma\n"} {
		if err := s.Append([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(t.Context(), End{}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		opts storage.ReadOpts
		want string
	}{
		{"all", storage.ReadOpts{}, "alpha\nkeep one\nbeta\nkeep two\ngamma\n"},
		{"head", storage.ReadOpts{Head: 2}, "alpha\nkeep one\n"},
		{"tail", storage.ReadOpts{Tail: 2}, "keep two\ngamma\n"},
		{"grep", storage.ReadOpts{Grep: "keep"}, "keep one\nkeep two\n"},
		{"range", storage.ReadOpts{Lines: "2:4"}, "keep one\nbeta\nkeep two\n"},
		{"ordered", storage.ReadOpts{Lines: "2:5", Grep: "keep", Tail: 1, Head: 1}, "keep two\n"},
		{"empty", storage.ReadOpts{Grep: "absent"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, got := readAll(t, objs, test.opts); got != test.want {
				t.Fatalf("read = %q, want %q", got, test.want)
			}
		})
	}
	if _, _, err := readLog(t, objs, storage.ReadOpts{Lines: "invalid"}); err == nil {
		t.Fatal("invalid line range succeeded")
	}
}

type stalledUploader struct {
	*memUploader
	started chan struct{}
}

func (u stalledUploader) PutSegment(ctx context.Context, _ Segment, _ []byte) error {
	select {
	case u.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

// An upload that never answers must not hold Close past its context: the
// close budget is what bounds how long a finishing node waits on its log.
func TestCloseGivesUpOnAStalledUploadWhenItsContextEnds(t *testing.T) {
	up := stalledUploader{memUploader: newMemUploader(newMemObjects(), testPrefix), started: make(chan struct{}, 1)}
	timer := &manualTimer{}
	s := newSink(t, up, Options{SegmentBytes: int64(len(line(0))), AfterFunc: timer.after})
	if err := s.Append([]byte(line(1))); err != nil {
		t.Fatal(err)
	}
	<-up.started
	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan Manifest, 1)
	go func() {
		m, _ := s.Close(ctx, End{})
		closed <- m
	}()
	cancel()
	if m := <-closed; m.Status != statusIncomplete || m.Reason != reasonUploadFailed || len(m.Segments) != 0 {
		t.Fatalf("manifest after a stalled upload = %+v, want incomplete with the segment missing", m)
	}
}

func TestSpoolWriteFailureCannotSealComplete(t *testing.T) {
	up := newMemUploader(newMemObjects(), testPrefix)
	sink := newSink(t, up, Options{})
	if err := sink.spool.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := os.Open(sink.spool.Name())
	if err != nil {
		t.Fatal(err)
	}
	sink.spool = readOnly
	if err := sink.Append([]byte("lost\n")); err == nil {
		t.Fatal("read-only spool accepted bytes")
	}
	manifest, err := sink.Close(t.Context(), End{})
	if err != nil {
		t.Fatalf("closing after failed append: %v", err)
	}
	if manifest.Status != statusIncomplete || manifest.Reason != "log persistence failed" {
		t.Fatalf("failed write sealed complete: %+v", manifest)
	}
}

func TestSpoolCloseFailureCannotSealComplete(t *testing.T) {
	sink := newSink(t, newMemUploader(newMemObjects(), testPrefix), Options{})
	if err := sink.spool.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := sink.Close(t.Context(), End{})
	if err == nil || manifest.Status != statusIncomplete {
		t.Fatalf("failed close sealed complete: %+v %v", manifest, err)
	}
}
