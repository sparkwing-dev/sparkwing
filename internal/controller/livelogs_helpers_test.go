package controller

// append adds one batch of log bytes to a node's ring and wakes every
// reader waiting on it.
func (l *liveLogs) append(runID, nodeID string, data []byte) {
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

// finish marks a node's ring complete. Readers still draining it see
// the end of the stream, and the sweep releases it shortly after.
func (l *liveLogs) finish(runID, nodeID string) {
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

// read returns everything buffered after since. The second result is
// false when no ring exists for the node, which is what a caller reads
// as "this node has no live log; use the durable copy".
func (l *liveLogs) read(runID, nodeID string, since int64) (liveChunk, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(l.clock())
	return l.readLocked(liveKey{runID, nodeID}, since)
}

// stats reports the ring count and buffered bytes, which is what a
// memory-cap test asserts on.
func (l *liveLogs) stats() (nodes int, bytes int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.nodes), l.total
}
