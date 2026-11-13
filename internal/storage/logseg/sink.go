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

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// Cutting bounds and the per-node byte cap. A segment is cut when
// segmentInterval has passed since the last cut with bytes pending, or
// when segmentBytes are pending, whichever comes first.
const (
	segmentInterval       = 10 * time.Second
	segmentBytes          = 1 << 20
	maxNodeBytes    int64 = 64 << 20
)

// truncationMarker is the last line of a log that reached
// [maxNodeBytes]; the sink keeps nothing after it.
const truncationMarker = "[sparkwing] truncated: the node log reached its 64 MiB cap\n"

// Uploader stores one attempt's segments and manifest. The controller
// issues the URLs it writes through; the uploader owns that exchange.
type Uploader interface {
	// PutSegment stores seg's compressed body under segment number
	// seg.N. Writing the same number twice writes the same bytes.
	PutSegment(ctx context.Context, seg Segment, body []byte) error
	// PutManifest seals the persisted stream; identical replay succeeds and conflicting seals return errSealed.
	PutManifest(ctx context.Context, m Manifest) error
}

// errSealed rejects a final payload that conflicts with the persisted stream seal.
var errSealed = errors.New("logseg: the attempt already has a manifest")

// ErrClosed refuses an append to a sink that has written its manifest.
var ErrClosed = errors.New("logseg: the sink is closed")

// Options bind a [Sink] to one subject. Other zero fields take package defaults.
type Options struct {
	Subject blob.LogSubject
	// SpoolDir holds the spool file; the system temporary directory
	// when empty.
	SpoolDir     string
	Interval     time.Duration
	SegmentBytes int64
	MaxBytes     int64
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
		o.Interval = segmentInterval
	}
	if o.SegmentBytes <= 0 {
		o.SegmentBytes = segmentBytes
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = maxNodeBytes
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
	// IncompleteReason carries a source log's loss marker when the sink republishes bytes
	// another writer already failed to preserve, so the copy never reads as complete.
	IncompleteReason string
}

type cutSegment struct {
	seg Segment
	off int64
}

// Sink spools one node attempt's log on local disk and uploads it as
// numbered segments. It is safe for concurrent use.
type Sink struct {
	opts Options
	up   Uploader
	ctx  context.Context

	mu                sync.Mutex
	spool             *os.File
	written           int64
	lines             int64
	cutOff            int64
	cutLine           int64
	stopTimer         func() bool
	queue             []cutSegment
	nextN             int
	uploaded          []Segment
	truncated         bool
	persistenceFailed bool
	dropped           int64
	closed            bool

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
	if err := opts.Subject.Validate(); err != nil {
		return nil, err
	}
	opts.Subject = cloneSubject(opts.Subject)
	opts.defaults()
	spool, err := os.CreateTemp(opts.SpoolDir, "sparkwing-log-*.spool")
	if err != nil {
		return nil, fmt.Errorf("logseg: open spool: %w", err)
	}
	s := &Sink{
		opts: opts, up: up, ctx: context.WithoutCancel(ctx), spool: spool, cutLine: 1, nextN: 1,
		work: make(chan struct{}, 1), done: make(chan struct{}),
	}
	s.loopCtx, s.stopLoop = context.WithCancel(s.ctx)
	go s.uploadLoop()
	return s, nil
}

// Append stores data, one or more newline-terminated lines.
func (s *Sink) Append(data []byte) error {
	return s.append(data, true)
}

// AppendDiagnostic retains best-effort diagnostics without starting an upload timer.
// Real output, the segment size bound and sealing still cut the buffered bytes.
func (s *Sink) AppendDiagnostic(data []byte) error {
	return s.append(data, false)
}

func (s *Sink) append(data []byte, schedule bool) error {
	if len(data) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if data[len(data)-1] != '\n' {
		data = append(bytes.Clone(data), '\n')
	}
	n := int64(bytes.Count(data, []byte{'\n'}))
	if s.truncated {
		s.dropped += n
		return nil
	}
	if s.written+int64(len(data)) > s.opts.MaxBytes {
		s.truncated = true
		s.dropped += n
		data, n = []byte(truncationMarker), 1
	}
	if _, err := s.spool.Write(data); err != nil {
		s.persistenceFailed = true
		return fmt.Errorf("logseg: write spool: %w", err)
	}
	s.written += int64(len(data))
	s.lines += n
	if s.written-s.cutOff >= s.opts.SegmentBytes {
		s.cutLocked()
	} else if schedule && s.stopTimer == nil {
		s.stopTimer = s.opts.AfterFunc(s.opts.Interval, s.cutOnInterval)
	}
	return nil
}

// MarkIncomplete records discarded bytes when a sink opens after persistence recovers.
func (s *Sink) MarkIncomplete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistenceFailed = true
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
				"subject", s.opts.Subject, "segment", seg.N, "err", err)
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
		if err = put(); err == nil || errors.Is(err, errSealed) {
			return err
		}
	}
	return err
}

// Close cuts the last segment, uploads every segment still pending, and
// writes the manifest, which seals the log. It returns the manifest it
// wrote. errSealed means the controller sealed the attempt first.
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
	closeErr := s.spool.Close()
	if closeErr != nil {
		s.persistenceFailed = true
	}
	m := s.manifestLocked(allUploaded, end)
	s.mu.Unlock()
	err := s.retry(ctx, func() error { return s.up.PutManifest(ctx, m) })
	name := s.spool.Name()
	if rerr := os.Remove(name); rerr != nil {
		s.opts.Logger.Warn("remove log spool", "path", name, "err", rerr)
	}
	return m, errors.Join(err, closeErr)
}

// safety: the caller holds mu.
func (s *Sink) manifestLocked(allUploaded bool, end End) Manifest {
	m := Manifest{
		Version: manifestVersion, Subject: new(cloneSubject(s.opts.Subject)),
		SealedBy: sealedByNode, SealedAt: time.Now().UTC(),
		Segments: s.uploaded,
	}
	for _, seg := range s.uploaded {
		m.Lines += seg.Lines
		m.Bytes += seg.Bytes
		m.LastSeq = seg.LastSeq
	}
	if m.Lines > 0 {
		m.FirstSeq = 1
	}
	m.DroppedLines = s.dropped + (s.lines - m.Lines)
	switch {
	case s.persistenceFailed:
		m.Status, m.Reason = statusIncomplete, "log persistence failed"
	case !allUploaded:
		m.Status, m.Reason = statusIncomplete, reasonUploadFailed
	case end.IncompleteReason != "":
		m.Status, m.Reason = statusIncomplete, end.IncompleteReason
	case s.truncated:
		m.Status, m.Reason = statusCutOff, reasonByteCap
	case end.Stopped:
		m.Status, m.Reason = statusCutOff, reasonStopped
	default:
		m.Status = StatusComplete
	}
	return m
}
