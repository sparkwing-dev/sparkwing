package logseg_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

func newSink(t *testing.T, up logseg.Uploader, opts logseg.Options) *logseg.Sink {
	t.Helper()
	opts.SpoolDir = t.TempDir()
	if opts.Subject.Kind == "" {
		opts.Subject = keySubject("acme", "run-1", "build", 1, 1)
	}
	if opts.AfterFunc == nil {
		opts.AfterFunc = (&manualTimer{}).after
	}
	opts.RetryBase, opts.RetryMax = time.Millisecond, time.Millisecond
	s, err := logseg.NewSink(t.Context(), up, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func line(i int) string { return fmt.Sprintf(`{"msg":"line %03d"}`+"\n", i) }

func readLog(t *testing.T, objs *memObjects, opts storage.ReadOpts) (logseg.ReadResult, []byte, error) {
	t.Helper()
	var out bytes.Buffer
	reader := logseg.CommittedReader{Records: objs, Objects: objs}
	result, err := reader.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: objs.view.Subject}, &out)
	if err != nil {
		return result, out.Bytes(), err
	}
	body, err := opts.Apply(out.Bytes())
	return result, body, err
}

func readAll(t *testing.T, objs *memObjects, opts storage.ReadOpts) (logseg.ReadResult, string) {
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
	s := newSink(t, up, logseg.Options{SegmentBytes: 64})
	var want strings.Builder
	for i := 1; i <= 20; i++ {
		if err := s.Append([]byte(line(i)), "st", int64(i), int64(i)); err != nil {
			t.Fatal(err)
		}
		want.WriteString(line(i))
	}
	s.WriterSealed("st", 20, 0)
	m, err := s.Close(context.Background(), logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusComplete || m.Lines != 20 || len(m.Segments) < 2 {
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
	s := newSink(t, up, logseg.Options{AfterFunc: clock.after})
	if clock.fire() {
		t.Fatal("an idle sink armed the interval cut")
	}
	if err := s.Append([]byte(line(1)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if !clock.fire() {
		t.Fatal("pending bytes armed no interval cut")
	}
	waitStored(t, up, 1)
	if clock.fire() {
		t.Fatal("the interval cut rearmed with nothing pending")
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1) {
		t.Fatalf("read %q", got)
	}
}

func TestKilledRunnerRetainsOnlyCommittedSegments(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	s := newSink(t, up, logseg.Options{SegmentBytes: 1})
	if err := s.Append([]byte(line(1)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	waitStored(t, up, 1)
	up.kill()
	if err := s.Append([]byte(line(2)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(t.Context(), logseg.End{}); err == nil {
		t.Fatal("dead runner sealed")
	}
	result, got := readAll(t, objs, storage.ReadOpts{})
	if result.Complete || got != line(1) {
		t.Fatalf("read=%+v, %q", result, got)
	}
}

func TestSinkRecordsWriterGapsAndIgnoresRetries(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{})
	for _, r := range [][2]int64{{1, 2}, {1, 2}, {5, 5}} {
		if err := s.Append([]byte(strings.Repeat("x\n", int(r[1]-r[0]+1))), "st", r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	s.WriterSealed("st", 6, 0)
	m, err := s.Close(context.Background(), logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusIncomplete || m.Reason != logseg.ReasonSequenceGap || m.Lines != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if len(m.Gaps) != 2 || m.Gaps[0] != (logseg.Gap{From: 3, To: 4}) || m.Gaps[1] != (logseg.Gap{From: 6, To: 6}) {
		t.Fatalf("gaps = %+v", m.Gaps)
	}
}

func TestSinkStatusForUnsealedAndStoppedWriters(t *testing.T) {
	for name, tc := range map[string]struct {
		seal bool
		end  logseg.End
		want string
	}{
		"numbered writer never sealed": {end: logseg.End{}, want: logseg.ReasonUnsealed},
		"writer stopped":               {seal: true, end: logseg.End{Stopped: true}, want: logseg.ReasonStopped},
	} {
		t.Run(name, func(t *testing.T) {
			objs := newMemObjects()
			s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{})
			if err := s.Append([]byte("x\n"), "st", 1, 1); err != nil {
				t.Fatal(err)
			}
			if tc.seal {
				s.WriterSealed("st", 1, 0)
			}
			m, err := s.Close(context.Background(), tc.end)
			if err != nil {
				t.Fatal(err)
			}
			if m.Status != logseg.StatusCutOff || m.Reason != tc.want {
				t.Fatalf("manifest = %+v", m)
			}
		})
	}
}

func TestSinkStopsAtTheByteCap(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{MaxBytes: 30})
	for i := 1; i <= 4; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.Close(context.Background(), logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusCutOff || m.Reason != logseg.ReasonByteCap || m.DroppedLines != 3 {
		t.Fatalf("manifest = %+v", m)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1)+logseg.TruncationMarker {
		t.Fatalf("read %q", got)
	}
}

func TestSinkReportsFailedUploads(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	up.failSeg = func(n int) bool { return n == 2 }
	s := newSink(t, up, logseg.Options{SegmentBytes: int64(len(line(0)))})
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	m, err := s.Close(context.Background(), logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusIncomplete || m.Reason != logseg.ReasonUploadFailed || len(m.Segments) != 1 || m.DroppedLines != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	if _, got := readAll(t, objs, storage.ReadOpts{}); got != line(1) {
		t.Fatalf("read %q", got)
	}
}

func TestReaderStopsAtASegmentFailingItsDigest(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{SegmentBytes: int64(len(line(0)))})
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err != nil {
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
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{SegmentBytes: 1})
	for _, body := range []string{"alpha", "keep one\n", "beta\n", "keep two", "gamma\n"} {
		if err := s.Append([]byte(body), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(t.Context(), logseg.End{}); err != nil {
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

func TestSinkHandsTheLiveViewEachAppendAtItsOffset(t *testing.T) {
	objs := newMemObjects()
	var pushed []string
	var offsets []int64
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{
		MaxBytes: 2 * int64(len(line(0))),
		Live: func(off int64, data []byte) {
			offsets = append(offsets, off)
			pushed = append(pushed, string(data))
		},
	})
	s.SetWatched(true)
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	n := int64(len(line(0)))
	if len(offsets) != 3 || offsets[0] != 0 || offsets[1] != n || offsets[2] != 2*n || pushed[2] != logseg.TruncationMarker {
		t.Fatalf("live view got %q at %v", pushed, offsets)
	}
}

// An unwatched node pushes nothing live. When a viewer arrives, the
// sink first pushes everything after the last uploaded segment, so the
// live view joins the segments with no gap, then each new line; when the
// viewer leaves it stops.
func TestSinkPushesLiveOnlyWhileWatched(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	var mu sync.Mutex
	var pushed []string
	var offsets []int64
	n := int64(len(line(0)))
	s := newSink(t, up, logseg.Options{
		SegmentBytes: 2 * n,
		Live: func(off int64, data []byte) {
			mu.Lock()
			defer mu.Unlock()
			offsets = append(offsets, off)
			pushed = append(pushed, string(data))
		},
	})
	for i := 1; i <= 3; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	waitStored(t, up, 1)
	s.SetWatched(true)
	if err := s.Append([]byte(line(4)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	s.SetWatched(false)
	if err := s.Append([]byte(line(5)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pushed) != 2 || (offsets[0] != 0 && offsets[0] != 2*n) || pushed[1] != line(4) || offsets[1] != 3*n {
		t.Fatalf("live view got %q at %v, want a pending suffix then line 4 at %d", pushed, offsets, 3*n)
	}
	// safety: storage may signal before the sink records its upload, so the pending suffix can overlap it.
	if want := (line(1) + line(2) + line(3))[offsets[0]:]; pushed[0] != want {
		t.Fatalf("pending suffix = %q at %d, want %q", pushed[0], offsets[0], want)
	}
}

type stalledUploader struct {
	*memUploader
	started chan struct{}
}

func (u stalledUploader) PutSegment(ctx context.Context, _ logseg.Segment, _ []byte) error {
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
	s := newSink(t, up, logseg.Options{SegmentBytes: int64(len(line(0))), AfterFunc: timer.after})
	if err := s.Append([]byte(line(1)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	<-up.started
	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan logseg.Manifest, 1)
	go func() {
		m, _ := s.Close(ctx, logseg.End{})
		closed <- m
	}()
	cancel()
	if m := <-closed; m.Status != logseg.StatusIncomplete || m.Reason != logseg.ReasonUploadFailed || len(m.Segments) != 0 {
		t.Fatalf("manifest after a stalled upload = %+v, want incomplete with the segment missing", m)
	}
}

func TestSinkRecordsWriterLoss(t *testing.T) {
	objects := newMemObjects()
	upload := newMemUploader(objects, testPrefix)
	sink := newSink(t, upload, logseg.Options{})
	if err := sink.Append([]byte(line(1)), "host", 1, 1); err != nil {
		t.Fatal(err)
	}
	sink.WriterSealed("host", 1, 2)
	manifest, err := sink.Close(t.Context(), logseg.End{})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != logseg.StatusIncomplete || manifest.DroppedLines != 2 {
		t.Fatalf("lost log sealed as complete: %+v", manifest)
	}
}
