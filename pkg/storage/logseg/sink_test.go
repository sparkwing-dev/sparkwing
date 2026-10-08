package logseg_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

const testPrefix = "logs/r90/teams/acme/runs/run-1/build/a1-g1/"

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
	opts.Run, opts.Node, opts.SpoolDir = "run-1", "build", t.TempDir()
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

func readAll(t *testing.T, objs *memObjects, opts storage.ReadOpts) (*logseg.Log, string) {
	t.Helper()
	l, err := logseg.Open(context.Background(), objs, testPrefix)
	if err != nil {
		t.Fatal(err)
	}
	body, err := l.Read(context.Background(), objs, opts)
	if err != nil {
		t.Fatal(err)
	}
	return l, string(body)
}

func waitStored(t *testing.T, up *memUploader, n int) {
	t.Helper()
	for got := range up.stored {
		if got >= n {
			return
		}
	}
}

var finished = logseg.NodeProgress{Started: true, Terminal: true}

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
	if c := l.Completeness(finished, time.Now()); c.State != logseg.StateComplete || c.Lines != 20 {
		t.Fatalf("completeness = %+v", c)
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

// A runner killed mid-node uploads nothing more: the segment it was
// filling never lands and it writes no manifest. The controller's seal
// keeps every segment that did land and the reader marks the log
// incomplete, so at most one segment is lost and never silently.
func TestRunnerKilledMidNodeLosesAtMostOneSegment(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	const segBytes = 100
	s := newSink(t, up, logseg.Options{SegmentBytes: segBytes})
	var written []string
	for i := 1; i <= 30; i++ {
		if err := s.Append([]byte(line(i)), "st", int64(i), int64(i)); err != nil {
			t.Fatal(err)
		}
		written = append(written, line(i))
	}
	waitStored(t, up, 5)
	up.kill()
	for i := 31; i <= 33; i++ {
		if err := s.Append([]byte(line(i)), "st", int64(i), int64(i)); err != nil {
			t.Fatal(err)
		}
		written = append(written, line(i))
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err == nil {
		t.Fatal("a dead runner wrote its manifest")
	}

	m, err := logseg.SealAbandoned(context.Background(), objs, testPrefix,
		logseg.Manifest{Run: "run-1", Node: "build", Attempt: 1, Generation: 1},
		logseg.ReasonRunnerLost, objs.putIfAbsent)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != logseg.StatusIncomplete || m.Reason != logseg.ReasonRunnerLost || m.SealedBy != logseg.SealedByController {
		t.Fatalf("controller seal = %+v", m)
	}
	l, got := readAll(t, objs, storage.ReadOpts{})
	all := strings.Join(written, "")
	if !strings.HasPrefix(all, got) {
		t.Fatalf("read %q is not a prefix of what the node wrote", got)
	}
	if lost := len(all) - len(got); lost > segBytes+len(line(0)) {
		t.Fatalf("lost %d bytes, more than one %d-byte segment", lost, segBytes)
	}
	c := l.Completeness(finished, time.Now())
	if c.State != logseg.StateIncomplete || !strings.Contains(c.Message, logseg.ReasonRunnerLost) {
		t.Fatalf("completeness = %+v", c)
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
	if c := logseg.Assess(&m, finished, time.Now()); c.MissingLines != 3 {
		t.Fatalf("completeness = %+v", c)
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
	objs.put(logseg.SegmentKey(testPrefix, 2), []byte("tampered"))
	l, got := readAll(t, objs, storage.ReadOpts{})
	if got != line(1) || l.Broken != 2 {
		t.Fatalf("read %q, broken %d", got, l.Broken)
	}
	if c := l.Completeness(finished, time.Now()); c.State != logseg.StateIncomplete {
		t.Fatalf("completeness = %+v", c)
	}
}

func TestTailReadFetchesOnlyTheSegmentsItNeeds(t *testing.T) {
	objs := newMemObjects()
	s := newSink(t, newMemUploader(objs, testPrefix), logseg.Options{SegmentBytes: int64(len(line(0)))})
	for i := 1; i <= 10; i++ {
		if err := s.Append([]byte(line(i)), "", 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Close(context.Background(), logseg.End{}); err != nil {
		t.Fatal(err)
	}
	objs.resetGets()
	_, got := readAll(t, objs, storage.ReadOpts{Tail: 2})
	if got != line(9)+line(10) {
		t.Fatalf("tail read %q", got)
	}
	if n := objs.getCount(); n != 3 {
		t.Fatalf("tail read made %d GETs, want the manifest and two segments", n)
	}
}

func TestNodeSealLosesToControllerSeal(t *testing.T) {
	objs := newMemObjects()
	up := newMemUploader(objs, testPrefix)
	s := newSink(t, up, logseg.Options{SegmentBytes: 1})
	if err := s.Append([]byte(line(1)), "", 0, 0); err != nil {
		t.Fatal(err)
	}
	waitStored(t, up, 1)
	if _, err := logseg.SealAbandoned(context.Background(), objs, testPrefix, logseg.Manifest{},
		logseg.ReasonRunnerLost, objs.putIfAbsent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Close(context.Background(), logseg.End{}); !errors.Is(err, logseg.ErrSealed) {
		t.Fatalf("Close after the controller sealed = %v, want ErrSealed", err)
	}
	l, _ := readAll(t, objs, storage.ReadOpts{})
	if l.Manifest.SealedBy != logseg.SealedByController {
		t.Fatalf("manifest = %+v", l.Manifest)
	}
}

func TestUnsealedLogStreamsUntilTheGraceEnds(t *testing.T) {
	now := time.Now()
	running := logseg.NodeProgress{Started: true}
	if c := logseg.Assess(nil, running, now); c.State != logseg.StateStreaming {
		t.Fatalf("running = %+v", c)
	}
	recent := logseg.NodeProgress{Started: true, Terminal: true, FinishedAt: now.Add(-time.Second)}
	if c := logseg.Assess(nil, recent, now); c.State != logseg.StateStreaming {
		t.Fatalf("just finished = %+v", c)
	}
	old := logseg.NodeProgress{Started: true, Terminal: true, FinishedAt: now.Add(-2 * logseg.SealGrace)}
	if c := logseg.Assess(nil, old, now); c.State != logseg.StateIncomplete {
		t.Fatalf("abandoned = %+v", c)
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
	if len(pushed) != 2 || pushed[0] != line(3) || offsets[0] != 2*n || pushed[1] != line(4) || offsets[1] != 3*n {
		t.Fatalf("live view got %q at %v, want line 3 at %d then line 4 at %d", pushed, offsets, 2*n, 3*n)
	}
}
