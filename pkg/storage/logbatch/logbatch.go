// Package logbatch coalesces per-node log appends into one object per
// flush. It wraps any [storage.LogStore] and exists for backends that
// turn every Append into its own object write, where a chatty node
// costs one request per line.
//
// A wrapped store buffers each (runID, nodeID) stream and hands the
// delegate one Append per flush. A flush happens when the buffer
// reaches [DefaultBufferThreshold] bytes, when the [DefaultFlushInterval]
// ticker finds the buffer non-empty, when a reader asks for the node's
// log, when the writer calls FlushNode at node finish, or at Close.
//
// Two caps bound what one node can write: [DefaultMaxObjects] flushes
// and [DefaultMaxBytes] buffered bytes. Past either cap the store
// drops further lines and writes one marker line, as the node's last
// object, naming how many it dropped.
//
// Wrap only backends whose Append is one object. A filesystem store
// appends to an open file and a logs service takes a streaming append,
// so neither gains anything from batching and both lose the immediacy
// their readers rely on.
package logbatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// DefaultFlushInterval bounds how long a buffered line waits before
// the delegate sees it.
const DefaultFlushInterval = 2 * time.Second

// DefaultBufferThreshold flushes a node's buffer early once it holds
// this many bytes.
const DefaultBufferThreshold = 256 << 10

// DefaultMaxObjects caps how many objects one node's log costs. The
// marker line that reports the drop is written past this cap, so a
// capped node writes one object more than the cap.
const DefaultMaxObjects = 2000

// DefaultMaxBytes caps how many log bytes one node writes.
const DefaultMaxBytes = 64 << 20

const flushTimeout = 10 * time.Second

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("logbatch: store is closed")

// Option configures a [Store].
type Option func(*Store)

// WithFlushInterval overrides [DefaultFlushInterval]. A non-positive
// interval keeps the default.
func WithFlushInterval(d time.Duration) Option {
	return func(s *Store) {
		if d > 0 {
			s.flushInterval = d
		}
	}
}

// WithBufferThreshold overrides [DefaultBufferThreshold]. A
// non-positive size keeps the default.
func WithBufferThreshold(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.bufferLimit = n
		}
	}
}

// WithMaxObjects overrides [DefaultMaxObjects]. A non-positive count
// keeps the default.
func WithMaxObjects(n int) Option {
	return func(s *Store) {
		if n > 0 {
			s.maxObjects = n
		}
	}
}

// WithMaxBytes overrides [DefaultMaxBytes]. A non-positive size keeps
// the default.
func WithMaxBytes(n int64) Option {
	return func(s *Store) {
		if n > 0 {
			s.maxBytes = n
		}
	}
}

// Store is a [storage.LogStore] that coalesces appends per node.
// The zero value is not usable; construct one with [New].
type Store struct {
	delegate storage.LogStore

	flushInterval time.Duration
	bufferLimit   int
	maxObjects    int
	maxBytes      int64

	mu      sync.Mutex
	nodes   map[nodeKey]*nodeBuf
	lost    map[nodeKey]nodeLoss
	closed  bool
	started bool

	stop chan struct{}
	done chan struct{}
}

type nodeKey struct {
	run  string
	node string
}

type nodeLoss struct {
	lines int
	bytes int64
}

type nodeBuf struct {
	mu        sync.Mutex
	buf       []byte
	objects   int
	bytes     int64
	dropped   int
	lostLines int
	lostBytes int64
	lastErr   error
}

// New wraps delegate so appends coalesce into one object per flush.
// The caller closes the returned store to flush what is still
// buffered and stop its flush goroutine; a node's own finish is
// cheaper to signal with [Store.FlushNode].
func New(delegate storage.LogStore, opts ...Option) *Store {
	s := &Store{
		delegate:      delegate,
		flushInterval: DefaultFlushInterval,
		bufferLimit:   DefaultBufferThreshold,
		maxObjects:    DefaultMaxObjects,
		maxBytes:      DefaultMaxBytes,
		nodes:         map[nodeKey]*nodeBuf{},
		lost:          map[nodeKey]nodeLoss{},
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

var (
	_ storage.LogStore = (*Store)(nil)
	_ io.Closer        = (*Store)(nil)
)

// LostOnFlush reports what a failed flush discarded for one node since
// the last call, and zeroes the counters. A batch a flush loses is not
// retried, so this is the only account of it; the writer folds it into
// the node's own dropped-line count.
func (s *Store) LostOnFlush(runID, nodeID string) (lines int, bytes int64) {
	key := nodeKey{runID, nodeID}
	s.mu.Lock()
	nb := s.nodes[key]
	if held, ok := s.lost[key]; ok {
		lines, bytes = held.lines, held.bytes
		delete(s.lost, key)
	}
	s.mu.Unlock()
	if nb == nil {
		return lines, bytes
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()
	lines += nb.lostLines
	bytes += nb.lostBytes
	nb.lostLines, nb.lostBytes = 0, 0
	return lines, bytes
}

// safety: FlushNode and Close drop the buffer, so what a failed flush
// lost has to outlive it or the writer never hears about the batch.
func (s *Store) retainLoss(key nodeKey, nb *nodeBuf) {
	if nb.lostLines == 0 && nb.lostBytes == 0 {
		return
	}
	s.mu.Lock()
	held := s.lost[key]
	held.lines += nb.lostLines
	held.bytes += nb.lostBytes
	s.lost[key] = held
	s.mu.Unlock()
	nb.lostLines, nb.lostBytes = 0, 0
}

var _ storage.FlushLossReporter = (*Store)(nil)

// Delegate returns the wrapped store, so a caller holding the batcher
// can still reach a capability the batcher does not forward.
func (s *Store) Delegate() storage.LogStore { return s.delegate }

// Append buffers data for (runID, nodeID). It returns the error of a
// flush it triggered, or the error a background flush recorded since
// the last call; a batch a flush failed to write is not retried,
// matching the drop an unbatched backend takes on a failed append.
func (s *Store) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	if err := storage.SafeLogIDs(runID, nodeID); err != nil {
		return fmt.Errorf("logbatch.Store.Append: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	key := nodeKey{runID, nodeID}
	//nolint:contextcheck // the flush loop belongs to the store, so one caller's context must not end every other node's buffer.
	nb, err := s.bufFor(key)
	if err != nil {
		return err
	}

	nb.mu.Lock()
	defer nb.mu.Unlock()
	if err := nb.takeErr(); err != nil {
		return err
	}
	if nb.cappedBy(s) {
		nb.dropped += countLines(data)
		return nil
	}
	nb.buf = append(nb.buf, data...)
	if data[len(data)-1] != '\n' {
		nb.buf = append(nb.buf, '\n')
	}
	nb.bytes += int64(len(data))
	if len(nb.buf) < s.bufferLimit {
		return nil
	}
	return s.flushLocked(ctx, key, nb)
}

// FlushNode writes everything buffered for one node, appends the drop
// marker when the node hit a cap, and releases its buffer. Writers
// call it when the node finishes so its last lines do not wait out the
// flush interval. Flushing a node that never wrote is not an error.
func (s *Store) FlushNode(ctx context.Context, runID, nodeID string) error {
	key := nodeKey{runID, nodeID}
	s.mu.Lock()
	nb := s.nodes[key]
	delete(s.nodes, key)
	s.mu.Unlock()
	if nb == nil {
		return nil
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()
	err := s.flushLocked(ctx, key, nb)
	if merr := s.writeDropMarker(ctx, key, nb); merr != nil && err == nil {
		err = merr
	}
	s.retainLoss(key, nb)
	return err
}

// Read flushes the node's buffer so the reader sees every line the
// writer has handed over, then delegates.
func (s *Store) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	if err := s.flushOne(ctx, nodeKey{runID, nodeID}); err != nil {
		return nil, err
	}
	return s.delegate.Read(ctx, runID, nodeID, opts)
}

// ReadRun flushes every buffered node of the run, then delegates.
func (s *Store) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	if err := s.flushRun(ctx, runID); err != nil {
		return nil, err
	}
	return s.delegate.ReadRun(ctx, runID)
}

// Stream delegates unchanged. A backend that does not stream still
// returns (nil, nil), and its callers still fall back to polling Read,
// which flushes.
func (s *Store) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	return s.delegate.Stream(ctx, runID, nodeID)
}

// DeleteRun discards the run's buffers and delegates.
func (s *Store) DeleteRun(ctx context.Context, runID string) error {
	s.mu.Lock()
	for key := range s.nodes {
		if key.run == runID {
			delete(s.nodes, key)
		}
	}
	for key := range s.lost {
		if key.run == runID {
			delete(s.lost, key)
		}
	}
	s.mu.Unlock()
	return s.delegate.DeleteRun(ctx, runID)
}

// Close stops the background flush and writes out every buffer still
// holding lines. Append after Close returns [ErrClosed]. Close is
// idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	started := s.started
	keys := make([]nodeKey, 0, len(s.nodes))
	bufs := make([]*nodeBuf, 0, len(s.nodes))
	for key, nb := range s.nodes {
		keys = append(keys, key)
		bufs = append(bufs, nb)
	}
	s.nodes = map[nodeKey]*nodeBuf{}
	s.mu.Unlock()

	if started {
		close(s.stop)
		<-s.done
	}

	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	var firstErr error
	for i, key := range keys {
		nb := bufs[i]
		nb.mu.Lock()
		if err := s.flushLocked(ctx, key, nb); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := s.writeDropMarker(ctx, key, nb); err != nil && firstErr == nil {
			firstErr = err
		}
		nb.mu.Unlock()
		s.retainLoss(key, nb)
	}
	return firstErr
}

func (s *Store) bufFor(key nodeKey) (*nodeBuf, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if !s.started {
		s.started = true
		go s.loop()
	}
	nb := s.nodes[key]
	if nb == nil {
		nb = &nodeBuf{}
		s.nodes[key] = nb
	}
	return nb, nil
}

func (s *Store) flushOne(ctx context.Context, key nodeKey) error {
	s.mu.Lock()
	nb := s.nodes[key]
	s.mu.Unlock()
	if nb == nil {
		return nil
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return s.flushLocked(ctx, key, nb)
}

func (s *Store) flushRun(ctx context.Context, runID string) error {
	keys, bufs := s.snapshot(runID)
	var firstErr error
	for i, key := range keys {
		nb := bufs[i]
		nb.mu.Lock()
		if err := s.flushLocked(ctx, key, nb); err != nil && firstErr == nil {
			firstErr = err
		}
		nb.mu.Unlock()
	}
	return firstErr
}

// safety: a sweep copies the buffers out first so it holds no store
// lock while it writes.
func (s *Store) snapshot(runID string) ([]nodeKey, []*nodeBuf) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]nodeKey, 0, len(s.nodes))
	bufs := make([]*nodeBuf, 0, len(s.nodes))
	for key, nb := range s.nodes {
		if runID != "" && key.run != runID {
			continue
		}
		keys = append(keys, key)
		bufs = append(bufs, nb)
	}
	return keys, bufs
}

// safety: callers hold nb.mu across the delegate write, which is what
// keeps two flushes of one node from interleaving and reordering it.
func (s *Store) flushLocked(ctx context.Context, key nodeKey, nb *nodeBuf) error {
	if err := nb.takeErr(); err != nil {
		return err
	}
	if len(nb.buf) == 0 {
		return nil
	}
	data := nb.buf
	nb.buf = nil
	if err := s.delegate.Append(ctx, key.run, key.node, data); err != nil {
		nb.lostLines += countLines(data)
		nb.lostBytes += int64(len(data))
		return fmt.Errorf("logbatch flush %s/%s: %w", key.run, key.node, err)
	}
	nb.objects++
	return nil
}

func (s *Store) writeDropMarker(ctx context.Context, key nodeKey, nb *nodeBuf) error {
	if nb.dropped == 0 {
		return nil
	}
	line := dropMarker(key.node, nb.dropped)
	nb.dropped = 0
	if err := s.delegate.Append(ctx, key.run, key.node, line); err != nil {
		return fmt.Errorf("logbatch drop marker %s/%s: %w", key.run, key.node, err)
	}
	nb.objects++
	return nil
}

func (s *Store) loop() {
	defer close(s.done)
	t := time.NewTicker(s.flushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.flushAll()
		}
	}
}

func (s *Store) flushAll() {
	keys, bufs := s.snapshot("")
	if len(keys) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	for i, key := range keys {
		nb := bufs[i]
		nb.mu.Lock()
		if len(nb.buf) > 0 {
			if err := s.flushLocked(ctx, key, nb); err != nil {
				nb.lastErr = err
			}
		}
		nb.mu.Unlock()
	}
}

func (nb *nodeBuf) takeErr() error {
	err := nb.lastErr
	nb.lastErr = nil
	return err
}

func (nb *nodeBuf) cappedBy(s *Store) bool {
	return nb.objects >= s.maxObjects || nb.bytes >= s.maxBytes
}

func countLines(data []byte) int {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if n == 0 {
		n = 1
	}
	return n
}

type markerRecord struct {
	TS    time.Time `json:"ts"`
	Level string    `json:"level"`
	Node  string    `json:"node,omitempty"`
	Msg   string    `json:"msg"`
}

// DropMarkerPrefix opens the message of the line a capped node ends
// with, so a reader can recognize a truncated log.
const DropMarkerPrefix = "log cap reached; dropped "

func dropMarker(nodeID string, dropped int) []byte {
	rec := markerRecord{
		TS:    time.Now().UTC(),
		Level: "warn",
		Node:  nodeID,
		Msg:   fmt.Sprintf("%s%d further line(s) for this node", DropMarkerPrefix, dropped),
	}
	line, err := json.Marshal(&rec)
	if err != nil {
		// safety: every field of markerRecord encodes, so the fallback is
		// unreachable; a log that admits truncation beats one that hides it.
		line = []byte(`{"level":"warn","msg":"log cap reached; further lines dropped"}`)
	}
	return append(line, '\n')
}
