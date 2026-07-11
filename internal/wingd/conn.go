package wingd

import (
	"bufio"
	"net"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// connRole is what a connection currently represents to the ledger.
type connRole int

const (
	roleNone connRole = iota
	roleWaiter
	roleHolder
)

// maxFrame bounds a single wire frame so a runaway peer cannot exhaust
// memory; the largest legitimate frame is a full queue-state dump.
const maxFrame = 8 << 20

// conn is one client connection. Its network I/O is self-contained; its
// admission role fields are guarded by the owning [Daemon]'s mutex.
type conn struct {
	d  *Daemon
	nc net.Conn
	sc *bufio.Scanner

	writeMu        sync.Mutex
	closeOnce      sync.Once
	disconnectOnce sync.Once

	runID     string
	pipeline  string
	pid       int
	role      connRole
	leaseID   admission.LeaseID
	members   []string
	resources wingwire.HostResources
	sems      []string
	startAt   time.Time

	// stalled and lowSince track the holder-idle verdict, guarded by the
	// owning Daemon's mutex. lowSince is when the holder's CPU first fell
	// below the stall threshold with waiters present; stalled latches once
	// that has held for the stall window.
	stalled  bool
	lowSince time.Time

	// finalizable marks a connection whose run row the daemon must
	// finalize when the connection drops while still holding or awaiting
	// admission: top-level run requests and child attaches, but never
	// semaphores-only sub-acquisitions from inside an admitted run.
	finalizable bool
}

func newConn(d *Daemon, nc net.Conn) *conn {
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	return &conn{d: d, nc: nc, sc: sc}
}

// readMessage blocks for the next framed message. It returns an error on
// EOF, a decode failure, or a closed connection.
func (c *conn) readMessage() (wingwire.Message, error) {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return nil, err
		}
		return nil, errPeerClosed
	}
	return wingwire.Decode(c.sc.Bytes())
}

// send serializes msg and writes it as one frame. Writes are serialized
// so frames from different goroutines never interleave.
func (c *conn) send(msg wingwire.Message) error {
	line, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.nc.Write(line)
	return err
}

// close shuts the underlying socket exactly once.
func (c *conn) close() {
	c.closeOnce.Do(func() { _ = c.nc.Close() })
}
