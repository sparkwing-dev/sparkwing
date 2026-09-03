package wingd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const defaultChargeCores = 1.0

const maxCancelledRunTombstones = 4096

type Daemon struct {
	cfg            Config
	layout         layout
	sampler        HostSampler
	procSampler    ProcSampler
	ownedSampler   OwnedCPUSampler
	guardInspector SessionGuardInspector

	lockFile *os.File
	ln       net.Listener

	connSeq atomic.Uint64

	ready       chan struct{}
	quit        chan struct{}
	shutdownOne sync.Once
	graceTimer  *time.Timer
	finalizers  sync.WaitGroup

	events eventWindow

	mu                  sync.Mutex
	persistMu           sync.Mutex
	persistedEventSeq   uint64
	persistWrite        func(string, admission.Snapshot, []admissionEvent, []string, []persistedGuard) error
	ledger              *admission.Ledger
	conns               map[*conn]struct{}
	byRun               map[string]*conn
	leaseRun            map[admission.LeaseID]string
	leaseCharge         map[admission.LeaseID]wingwire.HostResources
	leaseMembers        map[admission.LeaseID][]string
	reattachWait        map[admission.LeaseID]struct{}
	cancelPending       map[string]struct{}
	cancelledRuns       map[string]struct{}
	cancelledRunOrder   []string
	disconnectedPending map[string]struct{}
	guards              map[admission.LeaseID]*sessionGuardState
	draining            bool
	shuttingDown        bool
	lastActivity        time.Time
	startedAt           time.Time

	loadInit         bool
	externalInit     bool
	smoothedLoad     float64
	smoothedExternal float64
	headroomInit     bool
	appliedCores     float64
	appliedMem       uint64

	reservedCores float64
	externalCores float64
	reservedMem   uint64
	externalMem   uint64

	cpuMeasured bool
	memMeasured bool

	measuredAt time.Time
	headroomAt time.Time

	machineCores    float64
	machineMemory   uint64
	hostCores       float64
	hostMemory      uint64
	containerCores  float64
	containerMemory uint64
	budgetCores     float64
	budgetMemory    uint64

	capacityChange *wingwire.CapacityChange

	container *containerSensor

	cgroup *cgroupLimiter
}

type delivery struct {
	c   *conn
	msg wingwire.Message
}

func New(cfg Config) (*Daemon, error) {
	if cfg.Sampler != nil && cfg.OwnedCPUSampler != nil {
		if _, paired := cfg.Sampler.(pairedHostOwnedSampler); paired {
			return nil, fmt.Errorf("wingd: Config.Sampler and Config.OwnedCPUSampler both provide owned CPU accounting")
		}
	}
	lay, err := resolveLayout(cfg.Home)
	if err != nil {
		return nil, err
	}
	sampler := cfg.Sampler
	if sampler == nil {
		sampler = &platformSampler{}
		if cfg.OwnedCPUSampler != nil {
			sampler = hostSamplerOnly{HostSampler: sampler}
		}
	}
	procSampler := cfg.ProcSampler
	if procSampler == nil {
		procSampler = newProcSampler()
	}
	guardInspector := cfg.SessionGuardInspector
	if guardInspector == nil {
		guardInspector = processSessionInspector{}
	}
	ownedSampler := cfg.OwnedCPUSampler
	if ownedSampler == nil {
		ownedSampler = newOwnedCPUSampler()
	}
	return &Daemon{
		cfg:                 cfg,
		layout:              lay,
		sampler:             sampler,
		procSampler:         procSampler,
		ownedSampler:        ownedSampler,
		guardInspector:      guardInspector,
		container:           containerSensorFor(cfg),
		ready:               make(chan struct{}),
		quit:                make(chan struct{}),
		conns:               map[*conn]struct{}{},
		byRun:               map[string]*conn{},
		leaseRun:            map[admission.LeaseID]string{},
		leaseCharge:         map[admission.LeaseID]wingwire.HostResources{},
		leaseMembers:        map[admission.LeaseID][]string{},
		reattachWait:        map[admission.LeaseID]struct{}{},
		cancelPending:       map[string]struct{}{},
		cancelledRuns:       map[string]struct{}{},
		disconnectedPending: map[string]struct{}{},
		guards:              map[admission.LeaseID]*sessionGuardState{},
	}, nil
}

func (d *Daemon) Ready() <-chan struct{} { return d.ready }

func (d *Daemon) SocketPath() string { return d.layout.sock }

func (d *Daemon) Run(ctx context.Context) error {
	won, err := d.elect()
	if err != nil {
		return err
	}
	if !won {
		return ErrNotElected
	}
	defer d.releaseLock()
	defer func() {
		_ = os.Remove(d.layout.sock)
		_ = os.Remove(filepath.Dir(d.layout.sock))
	}()

	d.startedAt = d.now()
	if err := d.initLedger(); err != nil {
		return err
	}
	d.setupEnforcement()
	d.refreshHeadroom()

	ln, err := d.bindListener()
	if err != nil {
		return err
	}
	d.ln = ln
	d.startGrace()
	close(d.ready)
	d.cfg.logf("elected; serving %s (version %q)", d.layout.sock, d.cfg.Version)

	d.startDiagnostics(ctx.Done())

	go d.watchContext(ctx)
	go d.sampleLoop(ctx)
	go d.stallLoop(ctx)
	go d.idleLoop(ctx)
	go d.guardLoop(ctx.Done())

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
		if perr := checkPeerCredentials(nc); perr != nil {
			d.cfg.logf("%v", perr)
			_ = nc.Close()
			continue
		}
		c := newConn(d, nc)
		d.mu.Lock()
		d.conns[c] = struct{}{}
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
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("final persist: %v", err)
	}
	d.awaitFinalizers(FinalizeDrainWindow)
}

// safety: a finalize is the only record that an orphaned run ended, and the
// host closes the store as soon as Run returns, so an in-flight one is drained
// rather than left to fail against a closed handle.
func (d *Daemon) awaitFinalizers(within time.Duration) {
	done := make(chan struct{})
	go func() {
		d.finalizers.Wait()
		close(done)
	}()
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		d.cfg.logf("shutdown: a run finalize did not finish within %s", within)
	}
}

func (d *Daemon) finalizeAsync(runID string) {
	if d.cfg.Runs == nil {
		return
	}
	d.finalizers.Add(1)
	go func() {
		defer d.finalizers.Done()
		d.cfg.Runs.FinalizeRun(runID)
	}()
}

func (d *Daemon) initLedger() error {
	snap, events, cancelledRuns, guards, err := readStateWithGuards(d.layout.state)
	if err != nil {
		home := filepath.Dir(d.layout.dir)
		return fmt.Errorf("wingd: durable state %s is unreadable and may describe live guarded commands; after stopping them, run sparkwing daemon recover-state --yes --home %q: %w", d.layout.state, home, err)
	}
	if len(cancelledRuns) > maxCancelledRunTombstones {
		cancelledRuns = cancelledRuns[len(cancelledRuns)-maxCancelledRunTombstones:]
	}
	for _, runID := range cancelledRuns {
		if _, exists := d.cancelledRuns[runID]; exists {
			continue
		}
		d.recordCancelledRunLocked(runID)
	}
	d.events.restore(d.now(), events)
	stat, serr := d.sampler.Sample()
	if serr != nil {
		d.cfg.logf("initial host sample: %v", serr)
	}
	cap := d.deriveCapacity(stat)
	d.hostCores, d.hostMemory = cap.hostCores, cap.hostMemory
	d.machineCores, d.machineMemory = cap.machineCores, cap.machineMemory
	d.containerCores, d.containerMemory = cap.containerCores, cap.containerMemory
	d.budgetCores, d.budgetMemory = cap.budgetCores, cap.budgetMemory
	if d.containerCores > 0 || d.containerMemory > 0 {
		d.cfg.logf("container limit: %.1f cores, %s (host %.1f cores, %s)",
			d.machineCores, humanBytesLog(d.machineMemory), d.hostCores, humanBytesLog(d.hostMemory))
	}
	if d.cfg.Budget.HasCap() {
		d.cfg.logf("budget: %.1f cores, %s (machine %.1f cores, %s)",
			d.budgetCores, humanBytesLog(d.budgetMemory), d.machineCores, humanBytesLog(d.machineMemory))
	}
	if c := d.cfg.Budget.Cores; c > 0 && c > d.machineCores {
		d.cfg.logf("budget: requested %.1f cores exceeds machine %.1f; clamped to machine", c, d.machineCores)
	}
	if m := d.cfg.Budget.MemoryBytes; m > 0 && m > d.machineMemory {
		d.cfg.logf("budget: requested %s memory exceeds machine %s; clamped to machine", humanBytesLog(m), humanBytesLog(d.machineMemory))
	}
	if d.cfg.Budget.IgnoreExternal {
		d.cfg.logf("budget: ignoring external load in admission headroom (operator setting)")
	}
	var restored []admission.LeaseState
	if snap != nil {
		lg, kept, rerr := d.restoreLedger(*snap)
		if rerr != nil {
			if len(guards) > 0 {
				return fmt.Errorf("wingd: restore guarded authority: %w", rerr)
			}
			d.discardState(rerr)
		} else {
			d.ledger = lg
			restored = kept
		}
	}
	if d.ledger == nil {
		lg, err := admission.New(admission.Config{
			TotalCores:       d.budgetCores,
			TotalMemoryBytes: d.budgetMemory,
		})
		if err != nil {
			return fmt.Errorf("wingd: new ledger: %w", err)
		}
		d.ledger = lg
	}
	for _, ls := range restored {
		d.leaseRun[ls.ID] = ls.RequestID
		d.leaseCharge[ls.ID] = wingwire.HostResources{
			Cores:       float64(ls.MilliCores) / 1000.0,
			MemoryBytes: int64(ls.MemoryBytes),
		}
		d.leaseMembers[ls.ID] = append([]string(nil), ls.Members...)
		guard, guarded := persistedGuardForLease(guards, ls.ID)
		if guarded {
			if guard.RunID != ls.RequestID || !validGuardSession(guard.Session) {
				return fmt.Errorf("wingd: invalid guard for lease %s", ls.ID)
			}
			d.guards[ls.ID] = &sessionGuardState{persistedGuard: guard, disconnected: true}
		} else {
			d.reattachWait[ls.ID] = struct{}{}
		}
	}
	for _, guard := range guards {
		if _, ok := d.guards[guard.LeaseID]; !ok {
			return fmt.Errorf("wingd: guard names absent lease %s", guard.LeaseID)
		}
	}
	d.mu.Lock()
	d.lastActivity = d.now()
	d.mu.Unlock()
	return nil
}

func (d *Daemon) restoreLedger(snap admission.Snapshot) (*admission.Ledger, []admission.LeaseState, error) {
	lg, err := admission.Restore(snap, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("restore ledger: %w", err)
	}
	neededMilliCores := int64(0)
	neededMemory := uint64(0)
	for _, lease := range snap.Leases {
		neededMilliCores += lease.MilliCores
		neededMemory += lease.MemoryBytes
	}
	for _, waiter := range snap.Waiters {
		neededMilliCores = max(neededMilliCores, waiter.MilliCores)
		neededMemory = max(neededMemory, waiter.MemoryBytes)
	}
	applyCores := max(d.budgetCores, float64(neededMilliCores)/1000.0)
	applyMemory := max(d.budgetMemory, neededMemory)
	if err := lg.ResizeTotals(applyCores, applyMemory); err != nil {
		return nil, nil, fmt.Errorf("resize restored ledger: %w", err)
	}
	if _, err := lg.SetHeadroom(d.budgetCores, d.budgetMemory); err != nil {
		return nil, nil, fmt.Errorf("cap restored ledger headroom: %w", err)
	}
	return lg, snap.Leases, nil
}

func (d *Daemon) discardState(reason error) {
	dst, err := quarantineState(d.layout.state, d.now())
	if err != nil {
		d.cfg.logf("state file unusable (%v); quarantine failed: %v; serving with a fresh ledger", reason, err)
		return
	}
	d.cfg.logf("state file unusable: %v; quarantined to %s, serving with a fresh ledger", reason, dst)
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

func (d *Daemon) expireGrace() {
	d.mu.Lock()
	if d.shuttingDown {
		d.mu.Unlock()
		return
	}
	var events []admission.Event
	released := 0
	for id := range d.reattachWait {
		for _, m := range d.leaseMembers[id] {
			evs, err := d.ledger.Release(id, m)
			if err == nil {
				events = append(events, evs...)
			}
		}
		delete(d.reattachWait, id)
		released++
	}
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if released > 0 {
		d.cfg.logf("grace expired: released %d unreclaimed lease(s)", released)
	}
	d.flush(deliveries, snap)
}

func (d *Daemon) now() time.Time { return d.cfg.now() }

func (d *Daemon) touchLocked() { d.lastActivity = d.now() }

func (d *Daemon) touchConnLocked(c *conn) {
	if c.handshaked && !c.healthProbe {
		d.touchLocked()
	}
}

func (d *Daemon) isDrainingLocked() bool { return d.draining }

func (d *Daemon) serveConn(c *conn) {
	defer d.handleDisconnect(c)

	msg, err := c.readMessage()
	if err != nil {
		return
	}
	hello, ok := msg.(*wingwire.Hello)
	if !ok {
		return
	}
	d.mu.Lock()
	draining := d.isDrainingLocked()
	c.protocolMajor = wingwire.ServedMajor(hello.ProtocolMajor)
	served := c.protocolMajor
	c.healthProbe = hello.HealthProbe
	c.holderLiveness = hello.HolderLiveness
	c.handshaked = true

	if !c.healthProbe {
		d.touchLocked()
	}
	d.mu.Unlock()
	ack := &wingwire.HelloAck{
		ProtocolMajor:       served,
		NativeProtocolMajor: ProtocolMajor,
		BinaryVersion:       d.cfg.Version,
		BuildIdentity:       wingwire.BuildIdentity,
		Draining:            draining,
		StoreSchemaVersion:  d.cfg.StoreSchemaVersion,
		StoreRequirements:   d.cfg.StoreRequirements,
	}
	if d.cfg.Runs != nil {
		storeErr := d.cfg.Runs.Ready()
		storeReady := storeErr == nil
		ack.StoreReady = &storeReady
		if storeErr != nil {
			ack.StoreError = storeErr.Error()
		}
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

func (d *Daemon) dispatch(c *conn, msg wingwire.Message) bool {
	if c.healthProbe {
		if _, ok := msg.(*wingwire.QueueState); !ok {
			return true
		}
		d.handleQueueState(c)
		return false
	}
	switch m := msg.(type) {
	case *wingwire.AdmissionRequest:
		d.handleAdmission(c, m)
	case *wingwire.Reattach:
		d.handleReattach(c, m)
	case *wingwire.Release:
		d.handleRelease(c, m)
	case *wingwire.GuardComplete:
		d.handleGuardComplete(c, m)
	case *wingwire.LivenessAck:
		d.handleLivenessAck(c, m)
	case *wingwire.QueueState:
		d.handleQueueState(c)
	case *wingwire.CancelLease:
		d.handleCancelLease(c, m)
	case *wingwire.StatsReset:
		d.handleStatsReset(c)
	case *wingwire.DrainRequest:
		d.handleDrain(c, m)
		return true
	default:
		return true
	}
	return false
}

func (d *Daemon) handleLivenessAck(c *conn, ack *wingwire.LivenessAck) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c.role == roleHolder && c.livenessNonce == ack.Nonce {
		c.livenessNonce = 0
		c.stalled = false
		c.lowSince = d.now()
	}
}

func chargedResources(r wingwire.HostResources) wingwire.HostResources {
	if r.Cores == 0 && r.MemoryBytes == 0 {
		return wingwire.HostResources{Cores: defaultChargeCores}
	}
	return r
}

func (d *Daemon) clampHostChargeLocked(r wingwire.HostResources, costSource wingwire.CostSource) (wingwire.HostResources, bool) {
	if costSource != wingwire.CostSourcePin {
		if maxCores := d.idleGrantableCoresLocked(); maxCores > 0 && r.Cores > maxCores {
			r.Cores = maxCores
		}
		if maxMem := d.idleGrantableMemoryLocked(); maxMem > 0 && r.MemoryBytes > int64(maxMem) {
			r.MemoryBytes = int64(maxMem)
		}
	}
	return r, false
}

func strictCoreCostSource(costSource wingwire.CostSource) bool {
	return costSource == wingwire.CostSourcePin
}

func softCoreCostSource(costSource wingwire.CostSource) bool {
	switch costSource {
	case wingwire.CostSourceMeasured, wingwire.CostSourceDefault,
		wingwire.CostSourceMeasuring, wingwire.CostSourceFloor:
		return true
	default:
		return false
	}
}

func requestFromWire(runID, ownerRunID string, res wingwire.HostResources, sems []wingwire.SemaphoreClaim, costSource wingwire.CostSource, priority int) admission.Request {
	req := admission.Request{
		ID:          runID,
		OwnerID:     ownerRunID,
		Priority:    priority,
		Cores:       res.Cores,
		SoftCores:   softCoreCostSource(costSource),
		StrictCores: strictCoreCostSource(costSource),
	}
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

func (d *Daemon) idleGrantableCoresLocked() float64 {
	machineCores := d.machineCores
	if machineCores <= 0 {
		machineCores = d.budgetCores
	}
	if machineCores <= 0 {
		return 0
	}
	grantable := machineCores * (1 - d.cfg.headroomFraction())
	if d.budgetCores > 0 && d.budgetCores < grantable {
		grantable = d.budgetCores
	}
	if grantable < 0 {
		return 0
	}
	return grantable
}

func (d *Daemon) idleGrantableMemoryLocked() uint64 {
	machineMemory := d.machineMemory
	if machineMemory == 0 {
		machineMemory = d.budgetMemory
	}
	if machineMemory == 0 {
		return 0
	}
	grantable := uint64(float64(machineMemory) * (1 - d.cfg.headroomFraction()))
	if d.budgetMemory > 0 && d.budgetMemory < grantable {
		grantable = d.budgetMemory
	}
	return grantable
}

const subLeaseMajor = 2

const guardedSessionMajor = 3

func finalizesRun(protocolMajor int, req *wingwire.AdmissionRequest) bool {
	if protocolMajor < subLeaseMajor {
		return !req.SemaphoresOnly
	}
	return !req.SubLease
}

func (d *Daemon) terminalCheckReason(err error) string {
	version := d.cfg.Version
	if version == "" {
		version = "(unknown)"
	}
	return fmt.Sprintf("daemon %s could not read the runs store: %v", version, err)
}

func (d *Daemon) handleAdmission(c *conn, req *wingwire.AdmissionRequest) {
	if !validCostSource(req.CostSource) {
		d.rejectInvalid(c, req, rejectCauseCostSource, fmt.Sprintf(
			"admission request invalid: unrecognized cost source %q; pin resources explicitly with plan.Resources(sparkwing.Cores(n), sparkwing.MemoryGB(n)), or upgrade this box's sparkwing so its daemon knows the source",
			req.CostSource))
		return
	}
	if req.Guard != nil {
		if c.protocolMajor < guardedSessionMajor || req.ParentLeaseToken != "" || !validGuardSession(*req.Guard) {
			d.rejectInvalid(c, req, rejectCauseRequest, "admission request invalid: guarded session is unavailable for this request")
			return
		}
		if err := d.guardInspector.Validate(*req.Guard); err != nil {
			d.rejectInvalid(c, req, rejectCauseRequest, "admission request invalid: guarded session: "+err.Error())
			return
		}
		quiescent, err := d.guardInspector.Quiescent(*req.Guard)
		if err != nil || !quiescent {
			reason := "guarded session is not parked"
			if err != nil {
				reason = "guarded session inspection: " + err.Error()
			}
			d.rejectInvalid(c, req, rejectCauseRequest, "admission request invalid: "+reason)
			return
		}
	}
	d.mu.Lock()
	_, cancelled := d.cancelledRuns[req.RunID]
	d.mu.Unlock()
	if !cancelled && d.cfg.Runs != nil {
		var err error
		cancelled, err = d.cfg.Runs.IsRunTerminal(req.RunID)
		if err != nil {
			d.cfg.logf("admission: terminal check for %s: %v", req.RunID, err)
			_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "terminal-check", Policy: wingwire.PolicyFail, Reason: d.terminalCheckReason(err)})
			return
		}
	}
	if cancelled {
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
		return
	}
	if req.ParentLeaseToken != "" {
		d.handleChildAttach(c, req)
		return
	}
	requested := chargedResources(req.Resources)
	charged := requested

	d.mu.Lock()
	if _, cancelled := d.cancelledRuns[req.RunID]; cancelled {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
		return
	}
	if d.draining {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "draining", Policy: wingwire.Policy("draining")})
		return
	}
	pinClamped := false
	if req.SemaphoresOnly {
		charged = wingwire.HostResources{}
	} else {
		charged, pinClamped = d.clampHostChargeLocked(charged, req.CostSource)
	}
	req.OwnerRunID = d.validatedOwnerRunIDLocked(req.OwnerRunID, req.OwnerLeaseToken)
	ar := requestFromWire(req.RunID, req.OwnerRunID, charged, req.Semaphores, req.CostSource, req.Priority)
	c.runID = req.RunID
	c.ownerRunID = req.OwnerRunID
	c.displayRunID = req.DisplayRunID
	c.pipeline = req.Pipeline
	c.priority = req.Priority
	c.repo = req.Repo
	c.pid = req.PID
	if req.Guard != nil {
		guard := *req.Guard
		c.guard = &guard
	}
	c.resources = charged
	c.sems = semNames(req.Semaphores)
	c.finalizable = finalizesRun(c.protocolMajor, req)
	c.startAt = d.now()
	c.costSource = string(req.CostSource)
	c.expectedDurationMS = req.ExpectedDurationMS
	c.expectedP99MS = req.ExpectedP99MS
	c.sampleCount = req.SampleCount
	c.driftWarning = req.DriftWarning
	c.origin = req.Origin
	c.queueTimeoutMS = tightestQueueTimeoutMS(req.Semaphores)
	c.requestResources = requested
	c.requestSemaphores = cloneSemaphoreClaims(req.Semaphores)
	c.semaphoresOnly = req.SemaphoresOnly
	if existing := d.byRun[req.RunID]; existing != nil && existing != c {
		switch existing.role {
		case roleWaiter:
			if !requestIdentityMatches(existing, req, c.finalizable) ||
				existing.queueTimeoutMS != tightestQueueTimeoutMS(req.Semaphores) ||
				!queuedRequestPresent(d.ledger.Snapshot(), req.RunID) {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "duplicate", Policy: wingwire.PolicyFail})
				return
			}
			c.role = roleWaiter
			if !existing.startAt.IsZero() {
				c.startAt = existing.startAt
			}
			existing.role = roleNone
			existing.runID = ""
			existing.members = nil
			existing.finalizable = false
			d.byRun[req.RunID] = c
			events, err := d.ledger.ReplaceWaiter(ar)
			if err != nil {
				existing.role = roleWaiter
				existing.runID = req.RunID
				existing.finalizable = c.finalizable
				d.byRun[req.RunID] = existing
				c.role = roleNone
				c.runID = ""
				c.finalizable = false
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: submitErrorKey(err), Policy: wingwire.PolicyFail})
				return
			}
			deliveries := d.routeLocked(events)
			snap := d.ledger.Snapshot()
			if c.role == roleWaiter {
				if queued := d.queuedDeliveryLockedFromSnapshot(c, snap, req.RunID); queued != nil {
					deliveries = append(deliveries, *queued)
				}
			}
			d.touchLocked()
			d.mu.Unlock()
			existing.close()
			d.flush(deliveries, snap)
			return
		case roleHolder:
			if len(existing.members) != 1 {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "duplicate", Policy: wingwire.PolicyFail})
				return
			}
			snap := d.ledger.Snapshot()
			if !requestMetadataMatches(existing, req, c.finalizable) ||
				!grantedRequestPresent(snap, existing.leaseID, req.RunID) {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "duplicate", Policy: wingwire.PolicyFail})
				return
			}
			leaseID := existing.leaseID
			lease, ok := d.ledger.LeaseByID(leaseID)
			if !ok {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "duplicate", Policy: wingwire.PolicyFail})
				return
			}
			c.role = roleHolder
			c.leaseID = leaseID
			c.members = cloneStrings(existing.members)
			c.resources = existing.resources
			c.priority = existing.priority
			c.ownerRunID = existing.ownerRunID
			c.displayRunID = existing.displayRunID
			if !existing.startAt.IsZero() {
				c.startAt = existing.startAt
			}
			c.holdSampledMS = existing.holdSampledMS
			c.holdSaturatedMS = existing.holdSaturatedMS
			c.contended = existing.contended
			c.contentionReason = existing.contentionReason
			c.stalled = existing.stalled
			c.lowSince = existing.lowSince
			existing.role = roleNone
			existing.runID = ""
			existing.members = nil
			existing.finalizable = false
			for _, member := range c.members {
				d.byRun[member] = c
			}
			soleUnderLoad := d.soleRunUnderLoadLocked(c)
			externalCores := d.externalCores
			d.touchLocked()
			d.mu.Unlock()
			existing.close()
			grant := &wingwire.Grant{
				RunID:      req.RunID,
				LeaseToken: lease.Token,
				Resources:  c.resources,
				Semaphores: leaseSemaphores(snap, leaseID),
			}
			if soleUnderLoad {
				grant.SoleRunUnderLoad = true
				grant.ExternalCores = externalCores
			}
			_ = c.send(grant)
			return
		default:
			d.mu.Unlock()
			_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "duplicate", Policy: wingwire.PolicyFail})
			return
		}
	}
	d.byRun[req.RunID] = c
	dec, events, err := d.ledger.Submit(ar)
	if err != nil {
		delete(d.byRun, req.RunID)
		d.mu.Unlock()
		key := submitErrorKey(err)
		if key == "invalid" {
			d.rejectInvalid(c, req, rejectCauseRequest, "admission request invalid: "+err.Error())
			return
		}
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: key, Policy: wingwire.PolicyFail, Reason: refusalReason(err)})
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
	if pinClamped {
		d.cfg.logf("admission: run %s pinned %.1f cores exceeds grantable %.1f; running alone",
			req.RunID, req.Resources.Cores, charged.Cores)
	}
	if len(dec.Evicted) > 0 {
		d.cfg.logf("cancel_others: run %s superseded %d holder(s)", req.RunID, len(dec.Evicted))
		d.armCancelTimeout(dec.Evicted, cancelTimeoutFor(req.Semaphores))
	}
}

func (d *Daemon) validatedOwnerRunIDLocked(ownerRunID, ownerLeaseToken string) string {
	if ownerRunID == "" || ownerLeaseToken == "" {
		return ""
	}
	owner := d.byRun[ownerRunID]
	if owner == nil || !owner.finalizable || owner.role != roleHolder {
		return ""
	}
	if d.ledger.ProvesOwner(ownerLeaseToken, ownerRunID) {
		return ownerRunID
	}
	return ""
}

func validCostSource(source wingwire.CostSource) bool {
	switch source {
	case "", wingwire.CostSourcePin, wingwire.CostSourceMeasured, wingwire.CostSourceDefault,
		wingwire.CostSourceMeasuring, wingwire.CostSourceFloor:
		return true
	default:
		return false
	}
}

func tightestQueueTimeoutMS(sems []wingwire.SemaphoreClaim) int64 {
	var t int64
	for _, s := range sems {
		if s.QueueTimeoutMS <= 0 || (s.Policy != "" && s.Policy != wingwire.PolicyQueue) {
			continue
		}
		if t == 0 || s.QueueTimeoutMS < t {
			t = s.QueueTimeoutMS
		}
	}
	return t
}

func requestMetadataMatches(existing *conn, req *wingwire.AdmissionRequest, newFinalizable bool) bool {
	requested := chargedResources(req.Resources)
	return requestIdentityMatches(existing, req, newFinalizable) &&
		existing.costSource == string(req.CostSource) &&
		existing.expectedDurationMS == req.ExpectedDurationMS &&
		existing.expectedP99MS == req.ExpectedP99MS &&
		existing.sampleCount == req.SampleCount &&
		existing.driftWarning == req.DriftWarning &&
		existing.requestResources == requested &&
		existing.queueTimeoutMS == tightestQueueTimeoutMS(req.Semaphores)
}

func requestIdentityMatches(existing *conn, req *wingwire.AdmissionRequest, newFinalizable bool) bool {
	return existing.finalizable == newFinalizable &&
		existing.pipeline == req.Pipeline &&
		existing.repo == req.Repo &&
		existing.pid == req.PID &&
		processSessionMatches(existing.guard, req.Guard) &&
		existing.origin == req.Origin &&
		existing.priority == req.Priority &&
		existing.ownerRunID == req.OwnerRunID &&
		existing.displayRunID == req.DisplayRunID &&
		claimRequestsMatch(existing.requestSemaphores, req.Semaphores) &&
		existing.semaphoresOnly == req.SemaphoresOnly
}

func grantedRequestPresent(snap admission.Snapshot, leaseID admission.LeaseID, runID string) bool {
	for _, lease := range snap.Leases {
		if lease.ID != leaseID || lease.RequestID != runID {
			continue
		}
		return true
	}
	return false
}

func queuedRequestPresent(snap admission.Snapshot, runID string) bool {
	for _, w := range snap.Waiters {
		if w.RequestID != runID {
			continue
		}
		return true
	}
	return false
}

func cloneSemaphoreClaims(in []wingwire.SemaphoreClaim) []wingwire.SemaphoreClaim {
	if len(in) == 0 {
		return nil
	}
	return append([]wingwire.SemaphoreClaim(nil), in...)
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func claimRequestsMatch(got, want []wingwire.SemaphoreClaim) bool {
	if len(got) != len(want) {
		return false
	}
	for i, claim := range got {
		expected := want[i]
		if claim.Policy == "" {
			claim.Policy = wingwire.PolicyQueue
		}
		if expected.Policy == "" {
			expected.Policy = wingwire.PolicyQueue
		}
		if claim != expected {
			return false
		}
	}
	return true
}

func cancelTimeoutFor(sems []wingwire.SemaphoreClaim) time.Duration {
	var t time.Duration
	for _, s := range sems {
		if s.Policy != wingwire.PolicyCancelOthers || s.CancelTimeoutMS <= 0 {
			continue
		}
		d := time.Duration(s.CancelTimeoutMS) * time.Millisecond
		if t == 0 || d < t {
			t = d
		}
	}
	return t
}

func (d *Daemon) armCancelTimeout(evicted []admission.LeaseID, timeout time.Duration) {
	if timeout <= 0 || len(evicted) == 0 {
		return
	}
	leases := append([]admission.LeaseID(nil), evicted...)
	time.AfterFunc(timeout, func() { d.forceReleaseSuperseded(leases) })
}

func (d *Daemon) forceReleaseSuperseded(leases []admission.LeaseID) {
	d.mu.Lock()
	if d.shuttingDown {
		d.mu.Unlock()
		return
	}
	type supersededHolder struct {
		connection *conn
		guard      *persistedGuard
	}
	var holders []supersededHolder
	for _, id := range leases {
		rid, ok := d.leaseRun[id]
		if !ok {
			continue
		}
		if c := d.byRun[rid]; c != nil && c.leaseID == id && c.role == roleHolder {
			holder := supersededHolder{connection: c}
			if state := d.guards[id]; state != nil {
				guard := state.persistedGuard
				holder.guard = &guard
			}
			holders = append(holders, holder)
		} else if state := d.guards[id]; state != nil && state.disconnected {
			guard := state.persistedGuard
			holders = append(holders, supersededHolder{guard: &guard})
		}
	}
	d.mu.Unlock()
	for _, holder := range holders {
		runID := ""
		if holder.connection != nil {
			runID = holder.connection.runID
		} else if holder.guard != nil {
			runID = holder.guard.RunID
		}
		d.cfg.logf("cancel timeout: stopping superseded holder %s", runID)
		if holder.guard != nil {
			if err := d.guardInspector.Terminate(holder.guard.Session); err != nil {
				d.cfg.logf("cancel timeout: terminate guarded holder %s: %v", holder.guard.RunID, err)
			}
		}
		if holder.connection != nil {
			go d.handleDisconnect(holder.connection)
		}
	}
}

func (d *Daemon) handleChildAttach(c *conn, req *wingwire.AdmissionRequest) {
	d.mu.Lock()
	if _, pending := d.cancelPending[req.RunID]; pending {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
		return
	}
	if _, cancelled := d.cancelledRuns[req.RunID]; cancelled {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
		return
	}
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
	for _, lease := range d.ledger.Snapshot().Leases {
		if lease.ID != leaseID {
			continue
		}
		for _, member := range lease.Members {
			if _, pending := d.cancelPending[member]; pending {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
				return
			}
			if _, cancelled := d.cancelledRuns[member]; cancelled {
				d.mu.Unlock()
				_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "cancelled", Policy: wingwire.PolicyFail})
				return
			}
		}
		break
	}
	if err := d.ledger.Attach(leaseID, req.RunID); err != nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "parent", Policy: wingwire.PolicyFail})
		return
	}
	lease, _ := d.ledger.LeaseByID(leaseID)
	c.runID = req.RunID
	c.ownerRunID = req.OwnerRunID
	c.displayRunID = req.DisplayRunID
	c.pipeline = req.Pipeline
	c.repo = req.Repo
	c.pid = req.PID
	c.role = roleHolder
	c.leaseID = leaseID
	c.members = []string{req.RunID}
	c.startAt = d.now()
	c.finalizable = true
	c.resources = d.leaseCharge[leaseID]
	c.origin = req.Origin
	c.parentRun = d.leaseRun[leaseID]
	d.byRun[req.RunID] = c
	if existing, ok := d.leaseMembers[leaseID]; ok {
		d.leaseMembers[leaseID] = append(existing, req.RunID)
	}
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	_ = c.send(&wingwire.Grant{
		RunID:      req.RunID,
		LeaseToken: lease.Token,
		Resources:  c.resources,
		Semaphores: leaseSemaphores(snap, leaseID),
	})
}

func leaseSemaphores(snap admission.Snapshot, id admission.LeaseID) []string {
	for _, ls := range snap.Leases {
		if ls.ID != id {
			continue
		}
		return claimKeys(ls.Claims)
	}
	return nil
}

func (d *Daemon) handleReattach(c *conn, req *wingwire.Reattach) {
	d.mu.Lock()
	leaseID, err := d.ledger.Reattach(req.LeaseToken)
	if err != nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: c.runID, Key: "reattach", Policy: wingwire.PolicyFail})
		return
	}
	guard := d.guards[leaseID]
	_, pending := d.reattachWait[leaseID]
	if !pending && guard == nil {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: c.runID, Key: "reattach", Policy: wingwire.PolicyFail})
		return
	}
	members := append([]string(nil), d.leaseMembers[leaseID]...)
	if len(members) == 0 {
		for _, lease := range d.ledger.Snapshot().Leases {
			if lease.ID == leaseID {
				members = append(members, lease.Members...)
				break
			}
		}
	}
	d.mu.Unlock()
	for _, member := range members {
		if d.cfg.Runs == nil {
			continue
		}
		terminal, checkErr := d.cfg.Runs.IsRunTerminal(member)
		if checkErr != nil {
			d.cfg.logf("reattach: terminal check for %s: %v", member, checkErr)
			_ = c.send(&wingwire.Evicted{RunID: member, Key: "terminal-check", Policy: wingwire.PolicyFail, Reason: d.terminalCheckReason(checkErr)})
			return
		}
		if terminal {
			d.mu.Lock()
			d.recordCancelledRunLocked(member)
			d.mu.Unlock()
			_ = c.send(&wingwire.Evicted{RunID: member, Key: "cancelled", Policy: wingwire.PolicyFail})
			return
		}
	}
	d.mu.Lock()
	currentLeaseID, err := d.ledger.Reattach(req.LeaseToken)
	_, pending = d.reattachWait[leaseID]
	guard = d.guards[leaseID]
	if err != nil || currentLeaseID != leaseID || (!pending && guard == nil) ||
		(guard != nil && !guard.disconnected) {
		d.mu.Unlock()
		_ = c.send(&wingwire.Evicted{RunID: c.runID, Key: "reattach", Policy: wingwire.PolicyFail})
		return
	}
	if pending {
		delete(d.reattachWait, leaseID)
	}
	requestID := d.leaseRun[leaseID]
	c.role = roleHolder
	c.leaseID = leaseID
	c.runID = requestID
	c.startAt = d.now()
	c.finalizable = true
	c.resources = d.leaseCharge[leaseID]
	if members, ok := d.leaseMembers[leaseID]; ok {
		c.members = append([]string(nil), members...)
		if guard == nil {
			delete(d.leaseMembers, leaseID)
		}
	} else {
		c.members = []string{requestID}
	}
	if guard != nil {
		session := guard.Session
		c.guard = &session
		guard.disconnected = false
	}
	for _, m := range c.members {
		d.byRun[m] = c
	}
	lease, _ := d.ledger.LeaseByID(leaseID)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	d.cfg.logf("reattach: run %s reclaimed lease %s", requestID, leaseID)
	_ = c.send(&wingwire.Grant{RunID: requestID, LeaseToken: lease.Token, Resources: c.resources})
}

func (d *Daemon) handleRelease(c *conn, _ *wingwire.Release) {
	d.mu.Lock()
	if c.role != roleHolder {
		d.mu.Unlock()
		return
	}
	if d.guards[c.leaseID] != nil {
		d.mu.Unlock()
		c.close()
		return
	}
	events := d.releaseConnLocked(c)
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	d.flush(deliveries, snap)
}

func (d *Daemon) handleDrain(c *conn, req *wingwire.DrainRequest) {
	d.mu.Lock()
	d.draining = true
	remaining := len(d.leaseRun)
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	d.cfg.logf("draining for successor %s", req.SuccessorVersion)
	_ = c.send(&wingwire.DrainAck{HoldersRemaining: remaining})
	d.shutdown()
}

func (d *Daemon) handleCancelLease(c *conn, req *wingwire.CancelLease) {
	d.mu.Lock()
	target := d.byRun[req.RunID]
	if _, pending := d.cancelPending[req.RunID]; pending {
		d.mu.Unlock()
		c.close()
		return
	}
	if target == nil {
		guard := d.disconnectedGuardForRunLocked(req.RunID)
		if guard != nil {
			affected := guardLeaseMembers(d.ledger.Snapshot(), guard.LeaseID)
			for _, runID := range affected {
				d.cancelPending[runID] = struct{}{}
			}
			d.mu.Unlock()
			d.cancelDisconnectedGuard(c, guard.persistedGuard, affected)
			return
		}
	}
	if target == nil || !target.finalizable ||
		(target.role != roleHolder && target.role != roleWaiter) {
		d.mu.Unlock()
		_ = c.send(&wingwire.CancelLeaseAck{Found: false})
		return
	}
	waiter := target.role == roleWaiter
	affected := []string{req.RunID}
	if !waiter {
		for _, lease := range d.ledger.Snapshot().Leases {
			if lease.ID == target.leaseID {
				affected = append([]string(nil), lease.Members...)
				break
			}
		}
	}
	const reason = "cancelled via sparkwing runs cancel"
	for _, runID := range affected {
		d.cancelPending[runID] = struct{}{}
	}
	d.mu.Unlock()

	if d.cfg.Runs != nil {
		if err := d.cfg.Runs.FinalizeCancelledRuns(append([]string(nil), affected...), reason); err != nil {
			d.cfg.logf("cancel: finalize runs %s: %v", strings.Join(affected, ","), err)
			var orphaned []string
			d.mu.Lock()
			for _, runID := range affected {
				delete(d.cancelPending, runID)
				if _, disconnected := d.disconnectedPending[runID]; disconnected {
					delete(d.disconnectedPending, runID)
					orphaned = append(orphaned, runID)
				}
			}
			d.mu.Unlock()
			for _, runID := range orphaned {
				d.finalizeAsync(runID)
			}
			c.close()
			return
		}
	}

	d.mu.Lock()
	current := make(map[*conn]string)
	for _, runID := range affected {
		delete(d.cancelPending, runID)
		delete(d.disconnectedPending, runID)
		d.recordCancelledRunLocked(runID)
		if owner := d.byRun[runID]; owner != nil {
			if _, seen := current[owner]; !seen {
				current[owner] = runID
			}
		}
	}
	var events []admission.Event
	for owner := range current {
		switch owner.role {
		case roleWaiter:
			d.events.record(d.now(), admissionEvent{Kind: eventCancellation})
			events = append(events, d.cancelWaiterLocked(owner.runID)...)
			delete(d.byRun, owner.runID)
			owner.role = roleNone
			owner.finalizable = false
		case roleHolder:
			if d.guards[owner.leaseID] == nil {
				events = append(events, d.releaseConnLocked(owner)...)
			}
		}
	}
	deliveries := d.routeLocked(events)
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	persistErr := d.persistState(snap)
	if persistErr != nil {
		d.cfg.logf("cancel: persist tombstone: %v", persistErr)
	}
	for owner, runID := range current {
		d.cfg.logf("cancel: signalling run %s to wind down", runID)
		if err := owner.send(&wingwire.Cancel{RunID: runID, Reason: reason}); err != nil {
			c.close()
			return
		}
	}
	if persistErr != nil {
		c.close()
		return
	}
	for _, dl := range deliveries {
		if err := dl.c.send(dl.msg); err != nil {
			go d.handleDisconnect(dl.c)
		}
	}
	_ = c.send(&wingwire.CancelLeaseAck{Found: true})
}

func (d *Daemon) handleQueueState(c *conn) {
	d.mu.Lock()
	qs := d.buildQueueStateLocked()
	d.mu.Unlock()
	_ = c.send(&qs)
}

func (d *Daemon) handleStatsReset(c *conn) {
	d.events.reset()
	d.mu.Lock()
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("persist: %v", err)
	}
	d.cfg.logf("stats reset: admission-outcome window cleared")
	_ = c.send(&wingwire.StatsResetAck{})
}

func (d *Daemon) handleDisconnect(c *conn) {
	c.disconnectOnce.Do(func() {
		c.close()
		d.mu.Lock()

		role, runID := c.role, c.runID
		if runID == "" && len(c.members) > 0 {
			runID = c.members[0]
		}
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
		if c.role == roleHolder {
			if guard := d.guards[c.leaseID]; guard != nil {
				guard.disconnected = true
				if guard.completion == c {
					guard.completion = nil
				}
				d.touchConnLocked(c)
				d.mu.Unlock()
				d.logDisconnect(c, role, runID)
				return
			}
		}
		var orphaned []string
		if c.finalizable && d.cfg.Runs != nil {
			switch c.role {
			case roleHolder:
				for _, runID := range c.members {
					if _, pending := d.cancelPending[runID]; pending {
						d.disconnectedPending[runID] = struct{}{}
					} else {
						orphaned = append(orphaned, runID)
					}
				}
			case roleWaiter:
				if _, pending := d.cancelPending[c.runID]; pending {
					d.disconnectedPending[c.runID] = struct{}{}
				} else {
					orphaned = append(orphaned, c.runID)
				}
			}
		}
		var events []admission.Event
		switch c.role {
		case roleHolder:
			events = d.releaseConnLocked(c)
		case roleWaiter:
			if c.finalizable {
				d.events.record(d.now(), admissionEvent{Kind: waiterDepartureKindLocked(c, d.now())})
			}
			events = d.cancelWaiterLocked(c.runID)
		}
		deliveries := d.routeLocked(events)
		snap := d.ledger.Snapshot()
		d.touchConnLocked(c)
		d.mu.Unlock()
		d.logDisconnect(c, role, runID)
		for _, orphan := range orphaned {
			d.cfg.logf("orphan: conn %d lost run %s without release; finalizing", c.id, orphan)
			d.finalizeAsync(orphan)
		}
		d.flush(deliveries, snap)
	})
}

func (d *Daemon) logDisconnect(c *conn, role connRole, runID string) {
	if role == roleNone {
		return
	}
	if runID == "" {
		runID = "-"
	}
	d.cfg.logf("conn %d disconnected while %s (run %s)", c.id, role, runID)
}

func waiterDepartureKindLocked(c *conn, now time.Time) string {
	if c.queueTimeoutMS > 0 && !c.startAt.IsZero() &&
		now.Sub(c.startAt).Milliseconds() >= c.queueTimeoutMS {
		return eventQueueTimeout
	}
	return eventCancellation
}

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

func (d *Daemon) flush(deliveries []delivery, snap admission.Snapshot) {
	persistErr := d.persistState(snap)
	if persistErr != nil {
		d.cfg.logf("persist: %v", persistErr)
	}
	for _, dl := range deliveries {
		if persistErr != nil {
			if _, grant := dl.msg.(*wingwire.Grant); grant && dl.c.guard != nil {
				go d.handleDisconnect(dl.c)
				continue
			}
		}
		if err := dl.c.send(dl.msg); err != nil {
			go d.handleDisconnect(dl.c)
		}
	}
}

func (d *Daemon) recordCancelledRunLocked(runID string) {
	if _, exists := d.cancelledRuns[runID]; exists {
		return
	}
	d.cancelledRuns[runID] = struct{}{}
	d.cancelledRunOrder = append(d.cancelledRunOrder, runID)
	if len(d.cancelledRunOrder) <= maxCancelledRunTombstones {
		return
	}
	oldest := d.cancelledRunOrder[0]
	d.cancelledRunOrder = d.cancelledRunOrder[1:]
	delete(d.cancelledRuns, oldest)
}

func (d *Daemon) persistState(snap admission.Snapshot) error {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if snap.EventSeq < d.persistedEventSeq {
		return nil
	}
	d.mu.Lock()
	cancelledRuns := append([]string(nil), d.cancelledRunOrder...)
	guards := d.persistedGuardsLocked()
	d.mu.Unlock()
	write := d.persistWrite
	if write != nil {
		if err := write(d.layout.state, snap, d.events.snapshot(d.now()), cancelledRuns, guards); err != nil {
			return err
		}
	} else if err := writeStateWithGuards(d.layout.state, snap, d.events.snapshot(d.now()), cancelledRuns, guards); err != nil {
		return err
	}
	d.persistedEventSeq = snap.EventSeq
	return nil
}

const (
	rejectCauseCostSource = "cost_source"
	rejectCauseRequest    = "request"
)

func (d *Daemon) rejectInvalid(c *conn, req *wingwire.AdmissionRequest, cause, reason string) {
	_ = c.send(&wingwire.Evicted{RunID: req.RunID, Key: "invalid", Policy: wingwire.PolicyFail, Reason: reason})
	d.cfg.logf("conn %d rejected run %s: %s [cost_source=%q cores=%.2f memory_bytes=%d semaphores=%d]",
		c.id, req.RunID, reason, req.CostSource, req.Resources.Cores, req.Resources.MemoryBytes, len(req.Semaphores))
	d.events.record(d.now(), admissionEvent{Kind: eventRejection, Key: cause})
}

func refusalReason(err error) string {
	msg := err.Error()
	for _, sentinel := range []error{admission.ErrNeverAdmissible, admission.ErrDuplicateID} {
		if rest, ok := strings.CutPrefix(msg, sentinel.Error()+": "); ok {
			return rest
		}
	}
	return msg
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
