package nodeproto

import (
	"errors"
	"fmt"
	"strconv"
	"sync"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// Tracker assigns request ids and matches replies to them. Any number of
// requests may be pending, since run_step and eval requests run in parallel;
// which ops may overlap is the session's rule, not the tracker's. The zero
// value is ready to use and safe for concurrent use.
type Tracker struct {
	mu       sync.Mutex
	last     uint64
	pending  map[string]nodewire.Op
	answered map[string]bool
}

// Start returns a fresh id for a request of op and records it as pending.
func (t *Tracker) Start(op nodewire.Op) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending, t.answered = map[string]nodewire.Op{}, map[string]bool{}
	}
	t.last++
	id := strconv.FormatUint(t.last, 10)
	t.pending[id] = op
	return id
}

// Resolve marks the request r answers as done and returns its op, so the
// caller knows which result type to decode. A reply to an id never started
// fails with [ErrUnknownReply] and a second reply with [ErrDuplicateReply].
func (t *Tracker) Resolve(r nodewire.Reply) (nodewire.Op, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	op, ok := t.pending[r.ID]
	if !ok {
		if t.answered[r.ID] {
			return "", fmt.Errorf("%w: %s", ErrDuplicateReply, r.ID)
		}
		return "", fmt.Errorf("%w: %s", ErrUnknownReply, r.ID)
	}
	delete(t.pending, r.ID)
	t.answered[r.ID] = true
	return op, nil
}

// Pending returns the requests still waiting for a reply, by id, so a host
// whose process exited can fail each one.
func (t *Tracker) Pending() map[string]nodewire.Op {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]nodewire.Op, len(t.pending))
	for id, op := range t.pending {
		out[id] = op
	}
	return out
}

var (
	// ErrUnknownReply reports a reply to an id the host never sent.
	ErrUnknownReply = errors.New("nodeproto: reply to an unknown request")
	// ErrDuplicateReply reports a second reply to the same request.
	ErrDuplicateReply = errors.New("nodeproto: duplicate reply")
)
