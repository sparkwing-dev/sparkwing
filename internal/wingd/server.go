package wingd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// defaultChargeCores is the conservative host charge applied to a run
// that declared no resource hints, so unhinted work still counts against
// capacity rather than being admitted for free.
const defaultChargeCores = 1.0

// Daemon is one elected sparkwingd instance. Construct it with [New] and
// drive it with [Run]; it serves until it is drained, told to stop, or
// idles out.
type Daemon struct {
	cfg         Config
	layout      layout
	sampler     HostSampler
	procSampler ProcSampler

	lockFile *os.File
	ln       net.Listener

	ready       chan struct{}
	quit        chan struct{}
	shutdownOne sync.Once
	graceTimer  *time.Timer

	mu           sync.Mutex
	ledger       *admission.Ledger
	conns        map[*conn]struct{}
	byRun        map[string]*conn
	leaseRun     map[admission.LeaseID]string
	leaseCharge  map[admission.LeaseID]wingwire.HostResources
	leaseMembers map[admission.LeaseID][]string
	reattachWait map[admission.LeaseID]struct{}
	draining     bool
	shuttingDown bool
	lastActivity time.Time
	startedAt    time.Time

	loadInit     bool
	smoothedLoad float64
	headroomInit bool
	appliedCores float64
	appliedMem   uint64
}

// delivery pairs a framed message with the connection it belongs to.
type delivery struct {
	c   *conn
	msg wingwire.Message
}

// New constructs a daemon for cfg without electing or serving. Run does
// the election and blocks.
func New(cfg Config) (*Daemon, error) {
	lay, err := resolveLayout(cfg.Home)
	if err != nil {
		return nil, err
	}
	sampler := cfg.Sampler
	if sampler == nil {
		sampler = platformSampler{}
	}
	procSampler := cfg.ProcSampler
	if procSampler == nil {
		procSampler = newProcSampler()
	}
	return &Daemon{
		cfg:          cfg,
		layout:       lay,
		sampler:      sampler,
		procSampler:  procSampler,
		ready:        make(chan struct{}),
		quit:         make(chan struct{}),
		conns:        map[*conn]struct{}{},
		byRun:        map[string]*conn{},
		leaseRun:     map[admission.LeaseID]string{},
		leaseCharge:  map[admission.LeaseID]wingwire.HostResources{},
		leaseMembers: map[admission.LeaseID][]string{},
		reattachWait: map[admission.LeaseID]struct{}{},
	}, nil
}

// Ready returns a channel closed once the daemon is listening. Tests wait
// on it before connecting; it never fires for a daemon that lost the
// election.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// SocketPath is the address this daemon serves on.
func (d *Daemon) SocketPath() string { return d.layout.sock }

// Run elects, restores durable state, and serves until shutdown. It
// returns [ErrNotElected] immediately when another daemon owns this
// home, and nil on a clean stop (idle exit, drain, or context cancel).
func (d *Daemon) Run(ctx context.Context) error {
	won, err := d.elect()
	if err != nil {
		return err
	}
	if !won {
		return ErrNotElected
	}
	defer d.releaseLock()
	defer func() { _ = os.Remove(d.layout.sock) }()

	d.startedAt = d.now()
	if err := d.initLedger(); err != nil {
		return err
	}
	d.refreshHeadroom()

	ln, err := d.bindListener()
	if err != nil {
		return err
	}
	d.ln = ln
	d.startGrace()
	close(d.ready)

	go d.watchContext(ctx)
	go d.sampleLoop(ctx)
	go d.stallLoop(ctx)
	go d.idleLoop(ctx)

	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-d.quit:
				d.finalShutdown()
				return nil
			default:
				d.cfg.logf("accept: %v", err)
				d.finalShutdown()
				return nil
			}
		}
		c := newConn(d, nc)
		d.mu.Lock()
		d.conns[c] = struct{}{}
		d.touchLocked()
		d.mu.Unlock()
		go d.serveConn(c)
	}
}

func (d *Daemon) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		d.shutdown()
	case <-d.quit:
	}
}

// shutdown signals every loop to stop and closes the listener, which
// unblocks Accept. It is safe to call repeatedly.
func (d *Daemon) shutdown() {
	d.shutdownOne.Do(func() {
		d.mu.Lock()
		d.shuttingDown = true
		d.mu.Unlock()
		close(d.quit)
		if d.ln != nil {
			_ = d.ln.Close()
		}
	})
}

// finalShutdown closes every open connection without releasing leases --
// they persist for the successor to reclaim -- then writes a final
// snapshot.
func (d *Daemon) finalShutdown() {
	if d.graceTimer != nil {
		d.graceTimer.Stop()
	}
	d.mu.Lock()
	var toClose []*conn
	for c := range d.conns {
		toClose = append(toClose, c)
	}
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	for _, c := range toClose {
		c.close()
	}
	if err := writeState(d.layout.state, snap); err != nil {
		d.cfg.logf("final persist: %v", err)
	}
}

func (d *Daemon) initLedger() error {
	snap, err := readState(d.layout.state)
	if err != nil {
		return err
	}
	stat, serr := d.sampler.Sample()
	if serr != nil {
		d.cfg.logf("initial host sample: %v", serr)
	}
	if snap == nil {
		lg, err := admission.New(admission.Config{
			TotalCores:       stat.TotalCores,
			TotalMemoryBytes: stat.TotalMemoryBytes,
		})
		if err != nil {
			return fmt.Errorf("wingd: new ledger: %w", err)
		}
		d.ledger = lg
	} else {
		lg, err := admission.Restore(*snap, nil)
		if err != nil {
			return fmt.Errorf("wingd: restore ledger: %w", err)
		}
		d.ledger = lg
		for _, ls := range snap.Leases {
			d.leaseRun[ls.ID] = ls.RequestID
			d.leaseCharge[ls.ID] = wingwire.HostResources{
				Cores:       float64(ls.MilliCores) / 1000.0,
				MemoryBytes: int64(ls.MemoryBytes),
			}
			d.leaseMembers[ls.ID] = append([]string(nil), ls.Members...)
			d.reattachWait[ls.ID] = struct{}{}
		}
	}
	d.mu.Lock()
	d.lastActivity = d.now()
	d.mu.Unlock()
	return nil
}

func (d *Daemon) startGrace() {
	d.mu.Lock()
	pending := len(d.reattachWait)
	d.mu.Unlock()
	if pending == 0 {
		return
	}
	d.graceTimer = time.AfterFunc(d.cfg.graceWindow(), d.expireGrace)
}

// expireGrace releases every restored lease no client reclaimed within
// the grace window. Crash recovery and takeover both land here.
func (d *Daemon) expireGrace() {
	d.mu.Lock()
	if d.shuttingDown {
		d.mu.Unlock()
		return
	}
	var events []admission.Event
	for id := range d.reattachWait {
		for _, m := range d.leaseMembers[id] {
			evs, err := d.ledger.Release(id, m)
			if err == nil {
				events = append(events, evs...)
			}
		}
		delete(d.reattachWait, id)
	}
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	d.flush(deliveries, snap)
}

func (d *Daemon) now() time.Time { return d.cfg.now() }

func (d *Daemon) touchLocked() { d.lastActivity = d.now() }

func (d *Daemon) isDrainingLocked() bool { return d.draining }

// serveConn runs one connection: the version handshake, then the request
// loop, until the peer disconnects or the daemon shuts down.
func (d *Daemon) serveConn(c *conn) {
	defer d.handleDisconnect(c)

	msg, err := c.readMessage()
	if err != nil {
		return
	}
	if _, ok := msg.(*wingwire.Hello); !ok {
		return
	}
	d.mu.Lock()
	draining := d.isDrainingLocked()
	d.mu.Unlock()
	ack := &wingwire.HelloAck{
		ProtocolMajor: ProtocolMajor,
		BinaryVersion: d.cfg.Version,
		Draining:      draining,
	}
	if err := c.send(ack); err != nil {
		return
	}

	for {
		msg, err := c.readMessage()
		if err != nil {
			return
		}
		if d.dispatch(c, msg) {
			return
		}
	}
}

// dispatch handles one post-handshake message and reports whether the
// connection loop should stop.
func (d *Daemon) dispatch(c *conn, msg wingwire.Message) bool {
	switch m := msg.(type) {
	case *wingwire.AdmissionRequest:
		d.handleAdmission(c, m)
	case *wingwire.Reattach:
		d.handleReattach(c, m)
	case *wingwire.Release:
		d.handleRelease(c, m)
	case *wingwire.QueueState:
		d.handleQueueState(c)
	case *wingwire.DrainRequest:
		d.handleDrain(c, m)
		return true
	default:
		return true
	}
	return false
}

func chargedResources(r wingwire.HostResources) wingwire.HostResources {
	if r.Cores == 0 && r.MemoryBytes == 0 {
		return wingwire.HostResources{Cores: defaultChargeCores}
	}
	return r
}

func requestFromWire(runID string, res wingwire.HostResources, sems []wingwire.SemaphoreClaim) admission.Request {
	req := admission.Request{ID: runID, Cores: res.Cores}
	if res.MemoryBytes > 0 {
		req.MemoryBytes = uint64(res.MemoryBytes)
	}
	for _, s := range sems {
		req.Semaphores = append(req.Semaphores, admission.SemaphoreClaim{
			Key:      s.Name,
			Capacity: s.Capacity,
			Cost:     s.Cost,
			Policy:   admission.Policy(s.Policy),
		})
	}
	return req
}

func semNames(sems []wingwire.SemaphoreClaim) []string {
	if len(sems) == 0 {
		return nil
	}
	out := make([]string, 0, len(sems))
	for _, s := range sems {
		out = append(out, s.Name)
	}
	return out
}

// handleAdmission submits a run's all-or-nothing request. A granted or
// queued outcome is delivered through the event stream; fail and skip
// terminate the request with an [wingwire.Evicted] carrying the policy.
func (d *Daemon) handleAdmission(c *conn, req *wingwire.AdmissionRequest) {
	if req.ParentLeaseToken != "" {
		d.handleChildAttach(c, req)
		return
	}
	charged := chargedResources(req.Resources)
	if req.SemaphoresOnly {
		charged = wingwire.HostResources{}
	}
	ar := requestFromWire(req.RunID, charged, req.Semaphores)

	d.mu.Lock()
	if d.draining {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "draining", Policy: wingwire.Policy("draining")})
		return
	}
	c.runID = req.RunID
	c.pipeline = req.Pipeline
	c.pid = req.PID
	c.resources = charged
	c.sems = semNames(req.Semaphores)
	c.finalizable = !req.SemaphoresOnly
	c.startAt = d.now()
	c.costSource = req.CostSource
	c.expectedDurationMS = req.ExpectedDurationMS
	c.driftWarning = req.DriftWarning
	d.byRun[req.RunID] = c
	dec, events, err := d.ledger.Submit(ar)
	if err != nil {
		delete(d.byRun, req.RunID)
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: submitErrorKey(err), Policy: wingwire.PolicyFail})
		return
	}
	switch dec.Kind {
	case admission.DecisionQueued:
		c.role = roleWaiter
	case admission.DecisionFailed, admission.DecisionSkipped:
		delete(d.byRun, req.RunID)
		policy := wingwire.PolicyFail
		if dec.Kind == admission.DecisionSkipped {
			policy = wingwire.PolicySkip
		}
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: dec.Key, Policy: policy})
		return
	}
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	d.flush(deliveries, snap)
}

// handleChildAttach joins a child run to its parent's live lease so
// nested runs are not double-charged.
func (d *Daemon) handleChildAttach(c *conn, req *wingwire.AdmissionRequest) {
	d.mu.Lock()
	if d.draining {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "draining", Policy: wingwire.Policy("draining")})
		return
	}
	leaseID, err := d.ledger.Reattach(req.ParentLeaseToken)
	if err != nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "parent", Policy: wingwire.PolicyFail})
		return
	}
	if err := d.ledger.Attach(leaseID, req.RunID); err != nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "parent", Policy: wingwire.PolicyFail})
		return
	}
	lease, _ := d.ledger.LeaseByID(leaseID)
	c.runID = req.RunID
	c.pipeline = req.Pipeline
	c.pid = req.PID
	c.role = roleHolder
	c.leaseID = leaseID
	c.members = []string{req.RunID}
	c.startAt = d.now()
	c.finalizable = true
	d.byRun[req.RunID] = c
	if existing, ok := d.leaseMembers[leaseID]; ok {
		d.leaseMembers[leaseID] = append(existing, req.RunID)
	}
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if err := writeState(d.layout.state, snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	_ = c.send(&wingwire.Grant{RunID: req.RunID, LeaseToken: lease.Token, Semaphores: leaseSemaphores(snap, leaseID)})
}

// leaseSemaphores names every semaphore a lease holds, read from a
// ledger snapshot.
func leaseSemaphores(snap admission.Snapshot, id admission.LeaseID) []string {
	for _, ls := range snap.Leases {
		if ls.ID != id {
			continue
		}
		return claimKeys(ls.Claims)
	}
	return nil
}

// handleReattach reclaims a lease that survived a restart or takeover by
// re-binding this connection to it inside the grace window.
func (d *Daemon) handleReattach(c *conn, req *wingwire.Reattach) {
	d.mu.Lock()
	leaseID, err := d.ledger.Reattach(req.LeaseToken)
	if err != nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: c.runID, Key: "reattach", Policy: wingwire.PolicyFail})
		return
	}
	requestID := d.leaseRun[leaseID]
	c.role = roleHolder
	c.leaseID = leaseID
	c.runID = requestID
	c.startAt = d.now()
	c.resources = d.leaseCharge[leaseID]
	if members, ok := d.leaseMembers[leaseID]; ok {
		c.members = members
		delete(d.leaseMembers, leaseID)
	} else {
		c.members = []string{requestID}
	}
	for _, m := range c.members {
		d.byRun[m] = c
	}
	delete(d.reattachWait, leaseID)
	lease, _ := d.ledger.LeaseByID(leaseID)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if err := writeState(d.layout.state, snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	_ = c.send(&wingwire.Grant{RunID: requestID, LeaseToken: lease.Token, Resources: c.resources})
}

// handleRelease frees the lease this connection holds without waiting for
// the socket to close.
func (d *Daemon) handleRelease(c *conn, _ *wingwire.Release) {
	d.mu.Lock()
	if c.role != roleHolder {
		d.mu.Unlock()
		return
	}
	events := d.releaseConnLocked(c)
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	d.flush(deliveries, snap)
}

// handleDrain stops admission, acknowledges, and begins shutting the
// daemon down so a newer successor can take over its socket and state.
func (d *Daemon) handleDrain(c *conn, req *wingwire.DrainRequest) {
	d.mu.Lock()
	d.draining = true
	remaining := len(d.leaseRun)
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	if err := writeState(d.layout.state, snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	d.cfg.logf("draining for successor %s", req.SuccessorVersion)
	_ = c.send(&wingwire.DrainAck{HoldersRemaining: remaining})
	d.shutdown()
}

// handleQueueState answers a read-only state query. It creates no lease
// and leaves the connection open for the client to close.
func (d *Daemon) handleQueueState(c *conn) {
	d.mu.Lock()
	qs := d.buildQueueStateLocked()
	d.mu.Unlock()
	_ = c.send(&qs)
}

// handleDisconnect reacts to a connection ending. On a healthy daemon a
// holder's death releases its members and promotes waiters; a waiter's
// death removes it from the queue. During shutdown, leases are left
// intact for the successor.
func (d *Daemon) handleDisconnect(c *conn) {
	c.disconnectOnce.Do(func() {
		c.close()
		d.mu.Lock()
		delete(d.conns, c)
		for _, m := range c.members {
			if d.byRun[m] == c {
				delete(d.byRun, m)
			}
		}
		if c.runID != "" && d.byRun[c.runID] == c {
			delete(d.byRun, c.runID)
		}
		if d.shuttingDown {
			d.mu.Unlock()
			return
		}
		var orphaned []string
		if c.finalizable && d.cfg.FinalizeRun != nil {
			switch c.role {
			case roleHolder:
				orphaned = append(orphaned, c.members...)
			case roleWaiter:
				orphaned = append(orphaned, c.runID)
			}
		}
		var events []admission.Event
		switch c.role {
		case roleHolder:
			events = d.releaseConnLocked(c)
		case roleWaiter:
			events = d.cancelWaiterLocked(c.runID)
		}
		deliveries := d.routeLocked(events)
		snap := d.ledger.Snapshot()
		d.touchLocked()
		d.mu.Unlock()
		for _, runID := range orphaned {
			go d.cfg.FinalizeRun(runID)
		}
		d.flush(deliveries, snap)
	})
}

// releaseConnLocked releases every member the connection owns and clears
// its holder state. The caller holds d.mu.
func (d *Daemon) releaseConnLocked(c *conn) []admission.Event {
	var events []admission.Event
	for _, m := range c.members {
		evs, err := d.ledger.Release(c.leaseID, m)
		if err == nil {
			events = append(events, evs...)
		}
	}
	c.role = roleNone
	c.members = nil
	return events
}

// flush persists the post-mutation snapshot, then delivers queued frames.
// State is written before grants are announced so a re-attach token is
// durable before any client can act on it.
func (d *Daemon) flush(deliveries []delivery, snap admission.Snapshot) {
	if err := writeState(d.layout.state, snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	for _, dl := range deliveries {
		if err := dl.c.send(dl.msg); err != nil {
			go d.handleDisconnect(dl.c)
		}
	}
}

func submitErrorKey(err error) string {
	switch {
	case errors.Is(err, admission.ErrNeverAdmissible):
		return "never_admissible"
	case errors.Is(err, admission.ErrDuplicateID):
		return "duplicate"
	default:
		return "invalid"
	}
}
