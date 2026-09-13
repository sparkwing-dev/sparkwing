package controller

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// DefaultLiveLogNodeBytes is how much of a running node's log the
// controller keeps in memory for live readers.
const DefaultLiveLogNodeBytes = 512 << 10

// DefaultLiveLogTotalBytes caps the live log rings of every node
// together, so a burst across many concurrent nodes cannot grow the
// controller's heap without bound.
const DefaultLiveLogTotalBytes = 64 << 20

// DefaultLiveLogMaxNodes caps how many nodes hold a live buffer at
// once. Bytes alone do not bound the map, and a caller that can name a
// node can mint a buffer for it.
const DefaultLiveLogMaxNodes = 1024

// DefaultLiveLogIdleTimeout releases the ring of a node that stopped
// writing without ever reporting that it finished.
const DefaultLiveLogIdleTimeout = 10 * time.Minute

// safety: the idle sweep walks every ring under the registry lock, so it
// runs at most this often rather than on every append.
const liveLogSweepInterval = time.Second

// safety: a finished node's ring outlives the node by this much so a
// reader that connected late still sees its last lines.
const liveLogDrainGrace = 30 * time.Second

type liveKey struct {
	run  string
	node string
}

type liveRing struct {
	buf     []byte
	start   int64
	done    bool
	doneAt  time.Time
	touched time.Time
	updated chan struct{}
}

// safety: this is the only live view of a run whose durable logs
// surface has no live read, so its bounds are what keep a busy
// controller's heap flat.
type liveLogs struct {
	perNodeBytes int
	totalBytes   int64
	maxNodes     int
	idle         time.Duration

	mu        sync.Mutex
	nodes     map[liveKey]*liveRing
	total     int64
	lastSweep time.Time

	// safety: the sweep's clock, so a test can age a ring out without
	// waiting the idle timeout for it.
	now func() time.Time
}

func newLiveLogs() *liveLogs {
	return &liveLogs{
		perNodeBytes: DefaultLiveLogNodeBytes,
		totalBytes:   DefaultLiveLogTotalBytes,
		maxNodes:     DefaultLiveLogMaxNodes,
		idle:         DefaultLiveLogIdleTimeout,
		nodes:        map[liveKey]*liveRing{},
	}
}

func (l *liveLogs) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Append adds one batch of log bytes to a node's ring and wakes every
// reader waiting on it.
func (l *liveLogs) Append(runID, nodeID string, data []byte) {
	if len(data) == 0 {
		return
	}
	key := liveKey{runID, nodeID}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	l.sweepLocked(now)

	r := l.nodes[key]
	if r == nil {
		l.makeRoomLocked()
		r = &liveRing{updated: make(chan struct{})}
		l.nodes[key] = r
	}
	r.buf = append(r.buf, data...)
	if data[len(data)-1] != '\n' {
		r.buf = append(r.buf, '\n')
		l.total++
	}
	l.total += int64(len(data))
	r.touched = now
	l.trimNodeLocked(r)
	l.trimTotalLocked()
	l.broadcastLocked(r)
}

// Finish marks a node's ring complete. Readers still draining it see
// the end of the stream, and the sweep releases it shortly after.
func (l *liveLogs) Finish(runID, nodeID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.nodes[liveKey{runID, nodeID}]
	if r == nil || r.done {
		return
	}
	r.done = true
	r.doneAt = l.clock()
	l.broadcastLocked(r)
}

type liveChunk struct {
	Start int64
	Next  int64
	Data  []byte
	Done  bool
}

// Read returns everything buffered after since. The second result is
// false when no ring exists for the node, which is what a caller reads
// as "this node has no live log; use the durable copy".
func (l *liveLogs) Read(runID, nodeID string, since int64) (liveChunk, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(l.clock())
	return l.readLocked(liveKey{runID, nodeID}, since)
}

func (l *liveLogs) readLocked(key liveKey, since int64) (liveChunk, bool) {
	r := l.nodes[key]
	if r == nil {
		return liveChunk{}, false
	}
	if since < r.start {
		since = r.start
	}
	end := r.start + int64(len(r.buf))
	if since > end {
		since = end
	}
	data := bytes.Clone(r.buf[since-r.start:])
	return liveChunk{Start: since, Next: end, Data: data, Done: r.done}, true
}

// Wait blocks until the node has bytes after since, has finished, or
// ctx ends. It is the read half of the SSE stream.
func (l *liveLogs) Wait(ctx context.Context, runID, nodeID string, since int64) (liveChunk, bool) {
	key := liveKey{runID, nodeID}
	for {
		l.mu.Lock()
		chunk, ok := l.readLocked(key, since)
		if !ok || len(chunk.Data) > 0 || chunk.Done {
			l.mu.Unlock()
			return chunk, ok
		}
		wake := l.nodes[key].updated
		l.mu.Unlock()
		if wake == nil {
			return chunk, ok
		}
		select {
		case <-ctx.Done():
			return liveChunk{Start: since, Next: since}, true
		case <-wake:
		}
	}
}

// Stats reports the ring count and buffered bytes, which is what a
// memory-cap test asserts on.
func (l *liveLogs) Stats() (nodes int, bytes int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.nodes), l.total
}

func (l *liveLogs) broadcastLocked(r *liveRing) {
	if r.updated == nil {
		return
	}
	close(r.updated)
	r.updated = make(chan struct{})
}

func (l *liveLogs) releaseLocked(key liveKey, r *liveRing) {
	l.total -= int64(len(r.buf))
	if l.total < 0 {
		l.total = 0
	}
	r.done = true
	r.buf = nil
	delete(l.nodes, key)
	if r.updated != nil {
		close(r.updated)
		r.updated = nil
	}
}

func (l *liveLogs) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < liveLogSweepInterval {
		return
	}
	l.lastSweep = now
	for key, r := range l.nodes {
		expired := r.done && now.Sub(r.doneAt) > liveLogDrainGrace
		idle := !r.touched.IsZero() && now.Sub(r.touched) > l.idle
		if expired || idle {
			l.releaseLocked(key, r)
		}
	}
}

// safety: the ring count is what a caller who can name nodes controls,
// so a new ring evicts the least recently written one rather than
// growing the map.
func (l *liveLogs) makeRoomLocked() {
	for len(l.nodes) >= l.maxNodes {
		var oldestKey liveKey
		var oldest *liveRing
		for key, r := range l.nodes {
			if oldest == nil || r.touched.Before(oldest.touched) {
				oldestKey, oldest = key, r
			}
		}
		if oldest == nil {
			return
		}
		l.releaseLocked(oldestKey, oldest)
	}
}

func (l *liveLogs) trimNodeLocked(r *liveRing) {
	over := len(r.buf) - l.perNodeBytes
	if over <= 0 {
		return
	}
	l.dropOldestLocked(r, over)
}

func (l *liveLogs) trimTotalLocked() {
	for l.total > l.totalBytes {
		var widest *liveRing
		for _, r := range l.nodes {
			if widest == nil || len(r.buf) > len(widest.buf) {
				widest = r
			}
		}
		if widest == nil || len(widest.buf) == 0 {
			return
		}
		l.dropOldestLocked(widest, (len(widest.buf)+1)/2)
	}
}

// safety: the release rounds up to a line boundary, so a reader never
// sees half a line.
func (l *liveLogs) dropOldestLocked(r *liveRing, want int) {
	if want <= 0 {
		return
	}
	if want >= len(r.buf) {
		l.total -= int64(len(r.buf))
		r.start += int64(len(r.buf))
		r.buf = r.buf[:0]
		return
	}
	cut := want
	if i := bytes.IndexByte(r.buf[want:], '\n'); i >= 0 {
		cut = want + i + 1
	} else {
		cut = len(r.buf)
	}
	r.buf = append(r.buf[:0], r.buf[cut:]...)
	r.start += int64(cut)
	l.total -= int64(cut)
	if l.total < 0 {
		l.total = 0
	}
}
