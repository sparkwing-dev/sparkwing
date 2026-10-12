package logseg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Cutting bounds and the per-node byte cap. A segment is cut when
// SegmentInterval has passed since the last cut with bytes pending, or
// when SegmentBytes are pending, whichever comes first.
const (
	SegmentInterval       = 10 * time.Second
	SegmentBytes          = 1 << 20
	MaxNodeBytes    int64 = 64 << 20
)

// TruncationMarker is the last line of a log that reached
// [MaxNodeBytes]; the sink keeps nothing after it.
const TruncationMarker = "[sparkwing] truncated: the node log reached its 64 MiB cap\n"

const maxTrackedGaps = 32

// Uploader stores one attempt's segments and manifest. The controller
// issues the URLs it writes through; the uploader owns that exchange.
type Uploader interface {
	// PutSegment stores seg's compressed body under segment number
	// seg.N. Writing the same number twice writes the same bytes.
	PutSegment(ctx context.Context, seg Segment, body []byte) error
	// PutManifest writes m only if the attempt has no manifest yet, and
	// returns ErrSealed if it does.
	PutManifest(ctx context.Context, m Manifest) error
}

// ErrSealed refuses a manifest for an attempt that already has one,
// which the controller writes when it judged the attempt abandoned.
var ErrSealed = errors.New("logseg: the attempt already has a manifest")

// ErrClosed refuses an append to a sink that has written its manifest.
var ErrClosed = errors.New("logseg: the sink is closed")

// Options configure a [Sink]. Zero fields take the package defaults.
type Options struct {
	Run, Node string
	// SpoolDir holds the spool file; the system temporary directory
	// when empty.
	SpoolDir     string
	Interval     time.Duration
	SegmentBytes int64
	MaxBytes     int64
	// Live, when set, receives stored appends in order with the byte
	// offset in the log each begins at, for the controller's live view,
	// while a viewer watches the node (see [Sink.SetWatched]). It must
	// not block.
	Live func(offset int64, data []byte)
	// AfterFunc schedules the interval cut; time.AfterFunc when nil.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)
	// Retries is how many times a failed upload is tried before the
	// sink keeps its bytes and tries again at the next cut; 3 when zero.
	Retries   int
	RetryBase time.Duration
	RetryMax  time.Duration
	Logger    *slog.Logger
}

func (o *Options) defaults() {
	if o.Interval <= 0 {
		o.Interval = SegmentInterval
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = SegmentBytes
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = MaxNodeBytes
	}
	if o.AfterFunc == nil {
		o.AfterFunc = func(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }
	}
	if o.Retries <= 0 {
		o.Retries = 3
	}
	if o.RetryBase <= 0 {
		o.RetryBase = 200 * time.Millisecond
	}
	if o.RetryMax <= 0 {
		o.RetryMax = 2 * time.Second
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// End says how the node's writer finished.
type End struct {
	// Stopped reports a writer that did not finish on its own: it was
	// killed, cancelled, or never started.
	Stopped bool
}

type cutSegment struct {
	seg Segment
	off int64
}

type writerStream struct {
	next    int64
	gapSum  int64
	sealed  bool
	dropped int64
}

// Sink spools one node attempt's log on local disk and uploads it as
// numbered segments. It is safe for concurrent use.
type Sink struct {
	opts Options
	up   Uploader
	ctx  context.Context

	mu        sync.Mutex
	spool     *os.File
	written   int64
	lines     int64
	cutOff    int64
	cutLine   int64
	stopTimer func() bool
	queue     []cutSegment
	nextN     int
	uploaded  []Segment
	truncated bool
	dropped   int64
	streams   map[string]*writerStream
	gaps      []Gap
	extraGap  int64
	closed    bool
	watched   bool

	upMu     sync.Mutex
	work     chan struct{}
	done     chan struct{}
	loopCtx  context.Context
	stopLoop context.CancelFunc
}

// NewSink opens a spool file and starts the uploader. Close it to write
// the manifest and remove the spool. Uploads between cuts carry ctx's
// values but not its cancellation, so they outlive the request that
// opened the sink.
func NewSink(ctx context.Context, up Uploader, opts Options) (*Sink, error) {
	opts.defaults()
	spool, err := os.CreateTemp(opts.SpoolDir, "sparkwing-log-*.spool")
	if err != nil {
		return nil, fmt.Errorf("logseg: open spool: %w", err)
	}
	s := &Sink{
		opts: opts, up: up, ctx: context.WithoutCancel(ctx), spool: spool, cutLine: 1, nextN: 1,
		streams: map[string]*writerStream{},
		work:    make(chan struct{}, 1), done: make(chan struct{}),
	}
	s.loopCtx, s.stopLoop = context.WithCancel(s.ctx)
	go s.uploadLoop()
	return s, nil
}

// Append stores data, one or more newline-terminated lines. A writer
// that numbers its lines names its stream and the inclusive range first
// to last; a range the sink already stored is a retry and is ignored,
// and a range that skips ahead records a gap. An unnumbered writer
// passes an empty stream.
func (s *Sink) Append(data []byte, stream string, first, last int64) error {
	if len(data) == 0 {
		return nil
	}
	if last < first {
		last = first
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	var ws *writerStream
	if stream != "" && first > 0 {
		ws = s.stream(stream)
		if last < ws.next {
			return nil
		}
		if first > ws.next {
			s.recordGap(ws, ws.next, first-1)
		}
	}
	if data[len(data)-1] != '\n' {
		data = append(bytes.Clone(data), '\n')
	}
	n := int64(bytes.Count(data, []byte{'\n'}))
	if s.truncated {
		s.dropped += n
		s.advance(ws, last)
		return nil
	}
	if s.written+int64(len(data)) > s.opts.MaxBytes {
		s.truncated = true
		s.dropped += n
		data, n = []byte(TruncationMarker), 1
	}
	if _, err := s.spool.Write(data); err != nil {
		return fmt.Errorf("logseg: write spool: %w", err)
	}
	s.advance(ws, last)
	if s.watched && s.opts.Live != nil {
		s.opts.Live(s.written, data)
	}
	s.written += int64(len(data))
	s.lines += n
	if s.written-s.cutOff >= s.opts.SegmentBytes {
		s.cutLocked()
	} else if s.stopTimer == nil {
		s.stopTimer = s.opts.AfterFunc(s.opts.Interval, s.cutOnInterval)
	}
	return nil
}

func (s *Sink) stream(name string) *writerStream {
	ws := s.streams[name]
	if ws == nil {
		ws = &writerStream{next: 1}
		s.streams[name] = ws
	}
	return ws
}

func (s *Sink) advance(ws *writerStream, last int64) {
	if ws != nil {
		ws.next = last + 1
	}
}

func (s *Sink) recordGap(ws *writerStream, from, to int64) {
	ws.gapSum += to - from + 1
	if len(s.gaps) < maxTrackedGaps {
		s.gaps = append(s.gaps, Gap{From: from, To: to})
		return
	}
	s.extraGap += to - from + 1
}

// WriterSealed records a numbering writer's own closing statement: the
// last number it assigned and how many lines it gave up on.
func (s *Sink) WriterSealed(stream string, finalSeq, dropped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.stream(stream)
	if finalSeq >= ws.next {
		s.recordGap(ws, ws.next, finalSeq)
		ws.next = finalSeq + 1
	}
	ws.sealed = true
	ws.dropped = max(ws.dropped, dropped)
}

// SetWatched turns the live view on or off. Turning it on first hands
// Live every byte after the last uploaded segment, so the live view
// joins the segments with no gap; while it is off, nothing goes live.
func (s *Sink) SetWatched(watched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if watched == s.watched || s.closed {
		return
	}
	s.watched = watched
	if !watched || s.opts.Live == nil {
		return
	}
	var from int64
	for _, seg := range s.uploaded {
		from += seg.Bytes
	}
	if from >= s.written {
		return
	}
	pending := make([]byte, s.written-from)
	if _, err := s.spool.ReadAt(pending, from); err != nil {
		s.opts.Logger.Warn("read the log spool for the live view", "run_id", s.opts.Run, "node_id", s.opts.Node, "err", err)
		return
	}
	s.opts.Live(from, pending)
}

func (s *Sink) cutOnInterval() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopTimer = nil
	if !s.closed {
		s.cutLocked()
	}
}

// safety: the caller holds mu.
func (s *Sink) cutLocked() {
	if s.stopTimer != nil {
		s.stopTimer()
		s.stopTimer = nil
	}
	if s.written == s.cutOff {
		return
	}
	s.queue = append(s.queue, cutSegment{
		off: s.cutOff,
		seg: Segment{
			N: s.nextN, FirstSeq: s.cutLine, LastSeq: s.lines,
			Lines: s.lines - s.cutLine + 1, Bytes: s.written - s.cutOff,
		},
	})
	s.nextN++
	s.cutOff = s.written
	s.cutLine = s.lines + 1
	select {
	case s.work <- struct{}{}:
	default:
	}
}

func (s *Sink) uploadLoop() {
	defer close(s.done)
	for range s.work {
		s.drain(s.loopCtx)
	}
}

// safety: segments go up in order and a failed one stops the drain, so a listing never shows a later segment
// past a gap; the failed bytes stay in the spool for the next cut.
func (s *Sink) drain(ctx context.Context) bool {
	s.upMu.Lock()
	defer s.upMu.Unlock()
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return true
		}
		next := s.queue[0]
		s.mu.Unlock()
		seg, body, err := s.compress(next)
		if err == nil {
			err = s.retry(ctx, func() error { return s.up.PutSegment(ctx, seg, body) })
		}
		if err != nil {
			s.opts.Logger.Warn("log segment upload failed; the sink keeps it and tries again",
				"run_id", s.opts.Run, "node_id", s.opts.Node, "segment", seg.N, "err", err)
			return false
		}
		s.mu.Lock()
		s.queue = s.queue[1:]
		s.uploaded = append(s.uploaded, seg)
		s.mu.Unlock()
	}
}

func (s *Sink) compress(c cutSegment) (Segment, []byte, error) {
	raw := make([]byte, c.seg.Bytes)
	if _, err := s.spool.ReadAt(raw, c.off); err != nil {
		return c.seg, nil, fmt.Errorf("read spool: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return c.seg, nil, err
	}
	if err := zw.Close(); err != nil {
		return c.seg, nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	seg := c.seg
	seg.StoredBytes = int64(buf.Len())
	seg.SHA256 = hex.EncodeToString(sum[:])
	return seg, buf.Bytes(), nil
}

func (s *Sink) retry(ctx context.Context, put func() error) error {
	delay := s.opts.RetryBase
	var err error
	for try := 0; try < s.opts.Retries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			case <-time.After(delay):
			}
			delay = min(delay*2, s.opts.RetryMax)
		}
		if err = put(); err == nil || errors.Is(err, ErrSealed) {
			return err
		}
	}
	return err
}

// Close cuts the last segment, uploads every segment still pending, and
// writes the manifest, which seals the log. It returns the manifest it
// wrote. ErrSealed means the controller sealed the attempt first.
func (s *Sink) Close(ctx context.Context, end End) (Manifest, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Manifest{}, ErrClosed
	}
	s.closed = true
	s.cutLocked()
	close(s.work)
	s.mu.Unlock()
	// safety: an upload in flight when Close's context ends is cancelled, so a stalled store costs the
	// caller its close budget and no more; the manifest then records the segments that never landed.
	select {
	case <-s.done:
	case <-ctx.Done():
		s.stopLoop()
		<-s.done
	}
	s.stopLoop()
	allUploaded := s.drain(ctx)

	s.mu.Lock()
	m := s.manifestLocked(allUploaded, end)
	s.mu.Unlock()
	err := s.retry(ctx, func() error { return s.up.PutManifest(ctx, m) })
	name := s.spool.Name()
	if cerr := s.spool.Close(); cerr != nil {
		s.opts.Logger.Warn("close log spool", "path", name, "err", cerr)
	}
	if rerr := os.Remove(name); rerr != nil {
		s.opts.Logger.Warn("remove log spool", "path", name, "err", rerr)
	}
	return m, err
}

// safety: the caller holds mu.
func (s *Sink) manifestLocked(allUploaded bool, end End) Manifest {
	m := Manifest{
		Version: ManifestVersion, Run: s.opts.Run, Node: s.opts.Node,
		SealedBy: SealedByNode, SealedAt: time.Now().UTC(),
		Gaps: s.gaps, Segments: s.uploaded,
	}
	for _, seg := range s.uploaded {
		m.Lines += seg.Lines
		m.Bytes += seg.Bytes
		m.LastSeq = seg.LastSeq
	}
	if m.Lines > 0 {
		m.FirstSeq = 1
	}
	m.DroppedLines = s.dropped + s.extraGap + (s.lines - m.Lines)
	unsealed, writerDropped := false, false
	for _, ws := range s.streams {
		unsealed = unsealed || !ws.sealed
		if extra := ws.dropped - ws.gapSum; extra > 0 {
			m.DroppedLines += extra
			writerDropped = true
		}
	}
	switch {
	case !allUploaded:
		m.Status, m.Reason = StatusIncomplete, ReasonUploadFailed
	case s.truncated:
		m.Status, m.Reason = StatusCutOff, ReasonByteCap
	case end.Stopped:
		m.Status, m.Reason = StatusCutOff, ReasonStopped
	case unsealed:
		m.Status, m.Reason = StatusCutOff, ReasonUnsealed
	case len(s.gaps) > 0:
		m.Status, m.Reason = StatusIncomplete, ReasonSequenceGap
	case writerDropped:
		m.Status, m.Reason = StatusIncomplete, ReasonDropped
	default:
		m.Status = StatusComplete
	}
	return m
}
