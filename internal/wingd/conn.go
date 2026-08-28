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

type connRole int

const (
	roleNone connRole = iota
	roleWaiter
	roleHolder
)

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

const maxFrame = 8 << 20

const connWriteTimeout = 10 * time.Second

type conn struct {
	d  *Daemon
	nc net.Conn
	sc *bufio.Scanner

	id uint64

	writeMu        sync.Mutex
	closeOnce      sync.Once
	disconnectOnce sync.Once

	protocolMajor int

	healthProbe    bool
	holderLiveness bool

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

	parentRun string

	queueTimeoutMS int64

	costSource         string
	repo               string
	expectedDurationMS int64
	driftWarning       string
	origin             wingwire.Origin
	requestResources   wingwire.HostResources
	requestSemaphores  []wingwire.SemaphoreClaim
	semaphoresOnly     bool

	stalled  bool
	lowSince time.Time

	livenessNonce uint64
	livenessSeq   uint64

	expectedP99MS int64
	sampleCount   int

	holdSampledMS    int64
	holdSaturatedMS  int64
	contended        bool
	contentionReason string

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

func (c *conn) readMessage() (wingwire.Message, error) {
	if !c.sc.Scan() {
		if err := c.sc.Err(); err != nil {
			return nil, err
		}
		return nil, errPeerClosed
	}
	return wingwire.Decode(c.sc.Bytes())
}

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
			// safety: a partial timed-out frame corrupts framing; close the socket
			// and let disconnect handling release admission.
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

func (c *conn) close() {
	c.closeOnce.Do(func() { _ = c.nc.Close() })
}
