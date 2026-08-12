package wingd

import (
	"bufio"
	"errors"
	"net"
	"os"
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

// String renders the role for the daemon log.
func (r connRole) String() string {
	switch r {
	case roleWaiter:
		return "queued"
	case roleHolder:
		return "holding admission"
	default:
		return "idle"
	}
}

// maxFrame bounds a single wire frame so a runaway peer cannot exhaust
// memory; the largest legitimate frame is a full queue-state dump.
const maxFrame = 8 << 20

// connWriteTimeout bounds one frame write to a client. It is generous
// enough that no reading client meets it and short enough that a client
// which stopped reading is recognised as gone rather than waited on.
const connWriteTimeout = 10 * time.Second

// conn is one client connection. Its network I/O is self-contained; its
// admission role fields are guarded by the owning [Daemon]'s mutex.
type conn struct {
	d  *Daemon
	nc net.Conn
	sc *bufio.Scanner

	// id numbers this connection within the daemon's lifetime, so the
	// log lines a connection produces can be tied to each other and to
	// nothing else. Assigned once at construction and never written
	// again, which is why reading it needs no lock.
	id uint64

	writeMu        sync.Mutex
	closeOnce      sync.Once
	disconnectOnce sync.Once

	// protocolMajor is the wire major agreed for this connection at the
	// handshake, which is below the daemon's own when an older pinned SDK
	// is being served. Request fields added after that major are absent by
	// definition, so the handlers read the request in its terms.
	protocolMajor int

	// healthProbe marks a connection whose hello declared it a health
	// probe. It is left out of idle accounting -- it never advances
	// lastActivity and does not hold the daemon open -- so a daemon whose
	// only traffic is probes idles out and its supervisor reaps it. A
	// probe may only read queue state; dispatch drops it for anything
	// else, so the accounting exemption can never extend to admission.
	// Guarded by the owning Daemon's mutex.
	healthProbe bool

	// handshaked records that this connection's hello was read, which is
	// when it stops being an anonymous socket and starts being a client.
	// Idle accounting charges activity only to handshaked connections: a
	// peer that dialed and vanished without a hello did no work, and its
	// disconnect must not advance the idle clock -- a probe whose hello
	// was cut off by a deadline would otherwise reset the clock it
	// exists to leave alone. Guarded by the owning Daemon's mutex.
	handshaked bool

	runID        string
	ownerRunID   string
	displayRunID string
	pipeline     string
	priority     int
	pid          int
	guard        *wingwire.ProcessSession
	role         connRole
	leaseID      admission.LeaseID
	members      []string
	resources    wingwire.HostResources
	sems         []string
	startAt      time.Time

	// parentRun names the holder this connection's run attached to, for
	// child leases riding a parent's grant. Empty for top-level runs.
	parentRun string

	// queueTimeoutMS is the tightest bounded OnLimit:Queue wait the
	// request declared, kept so a waiter disconnect can be classified as
	// a queue timeout rather than a cancellation. Zero means unbounded.
	queueTimeoutMS int64

	// costSource, repo, expectedDurationMS, and driftWarning are display
	// metadata carried from the admission request into the queue view and
	// the ETA simulation. They are live-only: a daemon restart clears them
	// for reattached holders.
	costSource         string
	repo               string
	expectedDurationMS int64
	driftWarning       string
	origin             wingwire.Origin
	requestResources   wingwire.HostResources
	requestSemaphores  []wingwire.SemaphoreClaim
	semaphoresOnly     bool

	// stalled and lowSince track the holder-idle verdict, guarded by the
	// owning Daemon's mutex. lowSince is when the holder's CPU first fell
	// below the stall threshold with waiters present; stalled latches once
	// that has held for the stall window.
	stalled  bool
	lowSince time.Time

	// expectedP99MS and sampleCount carry the run's measured duration p99
	// and how many runs back it, from the admission request. The contention
	// detector requires a real p99 and a minimum sample count, so an
	// unprofiled or pinned-only run is never flagged. Display-metadata:
	// cleared for reattached holders after a daemon restart.
	expectedP99MS int64
	sampleCount   int

	// holdSampledMS and holdSaturatedMS accumulate, while this connection
	// holds admission, the host-sample time observed and the share of it
	// the host was saturated. contended latches the throttled verdict and
	// contentionReason explains it. All guarded by the owning Daemon's
	// mutex.
	holdSampledMS    int64
	holdSaturatedMS  int64
	contended        bool
	contentionReason string

	// finalizable marks a connection whose run row the daemon must
	// finalize when the connection drops while still holding or awaiting
	// admission: top-level run requests and child attaches, but never
	// semaphores-only sub-acquisitions from inside an admitted run.
	finalizable bool
}

func newConn(d *Daemon, nc net.Conn) *conn {
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	var id uint64
	if d != nil {
		id = d.connSeq.Add(1)
	}
	return &conn{d: d, nc: nc, sc: sc, id: id}
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
// so frames from different goroutines never interleave, and bounded by
// [connWriteTimeout] so a client that stopped reading cannot hold a
// daemon goroutine for the rest of its life.
func (c *conn) send(msg wingwire.Message) error {
	line, err := wingwire.Encode(msg)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.nc.SetWriteDeadline(time.Now().Add(connWriteTimeout)); err != nil {
		return err
	}
	if _, err := c.nc.Write(line); err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// safety: a timed-out write may have left a partial frame on the wire, so the stream can no longer be framed; close the socket and let the read loop's disconnect path release the connection's admission. The message names the connection rather than the run: runID is guarded by the daemon mutex, which this path does not hold. The conn id is immutable, so it is readable here and pairs this line with the disconnect line that follows it.
			c.logf("conn %d stopped reading; dropping connection after %s", c.id, connWriteTimeout)
			c.close()
		}
		return err
	}
	_ = c.nc.SetWriteDeadline(time.Time{})
	return nil
}

func (c *conn) logf(format string, args ...any) {
	if c.d == nil {
		return
	}
	c.d.cfg.logf(format, args...)
}

// close shuts the underlying socket exactly once.
func (c *conn) close() {
	c.closeOnce.Do(func() { _ = c.nc.Close() })
}
