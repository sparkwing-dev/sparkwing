package wingd

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const maxCancelledRunTombstones = 4096

type Daemon struct {
	admissionRevision uint64
	cfg               Config
	layout            layout
	sampler           HostSampler
	procSampler       ProcSampler
	ownedSampler      OwnedCPUSampler

	lockFile *os.File
	ln       net.Listener
	listen   func(string) (net.Listener, error)

	apiLn      net.Listener
	apiDone    chan struct{}
	apiErr     string
	managedAPI bool

	ready       chan struct{}
	quit        chan struct{}
	shutdownOne sync.Once
	connections sync.WaitGroup

	events      eventWindow
	journal     *journal.Writer
	incarnation uint64

	queueStateReads singleflight.Group

	mu                 sync.Mutex
	admissionContext   context.Context
	admissionStore     MachineAdmissionStore
	admissionTickets   map[string]*admissionTicket
	admissionKeys      map[string]string
	admissionCommitted []byte
	admissionChanged   chan struct{}
	processBirth       func(int) (string, error)
	ledger             *admission.Ledger
	conns              map[*conn]struct{}
	byRun              map[string]*conn
	leaseRun           map[admission.LeaseID]string
	leaseCharge        map[admission.LeaseID]wingwire.HostResources
	leaseMembers       map[admission.LeaseID][]string
	cancelledRuns      map[string]struct{}
	cancelledRunOrder  []string
	draining           bool
	runnerWake         runnerWake
	runnerChanges      sync.Mutex
	shuttingDown       bool
	localActivity      LocalActivity
	dashboardConns     int
	localControl       func() LocalStatus
	startedAt          time.Time

	loadInit             bool
	externalInit         bool
	unattributedInit     bool
	smoothedUnattributed float64
	smoothedLoad         float64
	smoothedExternal     float64
	headroomInit         bool
	appliedCores         float64
	appliedMem           uint64

	reservedCores      float64
	externalCores      float64
	externalAttributed bool
	reservedMem        uint64
	externalMem        uint64

	attribution  externalAttribution
	measuredPIDs map[int]struct{}

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
	msg any
}

func New(cfg Config) (*Daemon, error) {
	if cfg.Sampler != nil && cfg.OwnedCPUSampler != nil {
		if _, paired := cfg.Sampler.(pairedHostOwnedSampler); paired {
			return nil, fmt.Errorf("wingd: Config.Sampler and Config.OwnedCPUSampler both provide owned CPU accounting")
		}
	}
	lay, err := daemonLayout(cfg.Home)
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
	ownedSampler := cfg.OwnedCPUSampler
	if ownedSampler == nil {
		ownedSampler = newOwnedCPUSampler()
	}
	return &Daemon{
		cfg:              cfg,
		admissionContext: context.Background(),
		admissionTickets: map[string]*admissionTicket{},
		admissionKeys:    map[string]string{},
		admissionChanged: make(chan struct{}),
		layout:           lay,
		sampler:          sampler,
		procSampler:      procSampler,
		ownedSampler:     ownedSampler,
		container:        containerSensorFor(cfg),
		ready:            make(chan struct{}),
		quit:             make(chan struct{}),
		conns:            map[*conn]struct{}{},
		byRun:            map[string]*conn{},
		measuredPIDs:     map[int]struct{}{},
		leaseRun:         map[admission.LeaseID]string{},
		leaseCharge:      map[admission.LeaseID]wingwire.HostResources{},
		leaseMembers:     map[admission.LeaseID][]string{},
		cancelledRuns:    map[string]struct{}{},
	}, nil
}

func (d *Daemon) Ready() <-chan struct{} { return d.ready }

func (d *Daemon) Run(ctx context.Context) error {
	won, err := d.elect()
	if err != nil {
		return err
	}
	if !won {
		return ErrNotElected
	}
	defer d.releaseLock()
	return d.runElected(ctx)
}

func (d *Daemon) runElected(ctx context.Context) error {
	d.admissionContext = context.WithoutCancel(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	incarnation := uint64(rand.Int64())
	d.incarnation = incarnation
	d.journal = journal.NewWriter(d.layout.dir, incarnation, d.cfg.Logf)
	defer d.journal.Close()
	defer func() {
		if !d.managedAPI {
			d.removeSockets()
		}
	}()

	d.startedAt = d.now()
	if err := d.initLedger(); err != nil {
		d.recordJournal("shutdown", nil, map[string]any{"reason": "ledger initialization failed", "error": err.Error()})
		return err
	}
	startSnapshot := d.ledger.Snapshot()
	d.recordJournal("start", nil, map[string]any{"version": d.cfg.Version, "build_identity": wingwire.BuildIdentity, "restored_leases": len(startSnapshot.Leases), "restored_waiters": len(startSnapshot.Waiters), "budget": journalBudget(d.cfg.Budget), "policy": journalPolicy(d.cfg.admissionPolicy())})
	d.setupEnforcement()
	d.refreshHeadroom()

	ln, err := d.bindListener()
	if err != nil {
		d.recordJournal("shutdown", nil, map[string]any{"reason": "listener bind failed", "error": err.Error()})
		return err
	}
	d.ln = ln
	if err := d.startAPI(ctx); err != nil {
		d.shutdownWithReason("API bind failed")
		d.finalShutdown()
		return err
	}

	close(d.ready)
	d.recordJournal("ready", nil, map[string]any{"socket": d.layout.sock})
	d.cfg.logf("elected; serving %s (version %q)", d.layout.sock, d.cfg.Version)

	diagnosticsDone := d.startDiagnostics(ctx.Done())

	var workers sync.WaitGroup
	for _, run := range []func(context.Context){d.watchContext, d.sampleLoop, d.stallLoop, d.admissionLivenessLoop} {
		workers.Go(func() { run(ctx) })
	}
	defer func() {
		cancel()
		workers.Wait()
		<-diagnosticsDone
	}()

	for {
		nc, err := ln.Accept()
		if err != nil {
			select {
			case <-d.quit:
				d.finalShutdown()
				return nil
			default:
				d.cfg.logf("accept: %v", err)
				d.recordJournal("shutdown", nil, map[string]any{"reason": "accept failed", "error": err.Error()})
				d.finalShutdown()
				return nil
			}
		}
		if perr := checkPeerCredentials(nc); perr != nil {
			_ = nc.Close()
			continue
		}
		d.connections.Add(1)
		go func() {
			defer d.connections.Done()
			stop := context.AfterFunc(ctx, func() {
				if err := nc.Close(); err != nil {
					d.cfg.logf("close legacy peer: %v", err)
				}
			})
			defer stop()
			d.refuseLegacyAdmission(nc)
		}()

	}
}

func (d *Daemon) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		d.shutdownWithReason("context cancelled")
	case <-d.quit:
	}
}

func (d *Daemon) shutdownWithReason(reason string) {
	d.shutdownOne.Do(func() {
		d.recordJournal("shutdown", nil, map[string]any{"reason": reason})
		d.mu.Lock()
		d.shuttingDown = true
		d.mu.Unlock()
		close(d.quit)
		if !d.managedAPI {
			d.closeAPIListener()
		}
		if d.ln != nil {
			_ = d.ln.Close()
		}
	})
}

func (d *Daemon) finalShutdown() {
	d.mu.Lock()
	draining := d.draining
	d.mu.Unlock()
	if draining {
		d.recordJournal("drain_end", nil, nil)
	}
	d.connections.Wait()
	if err := d.persistState(); err != nil {
		d.cfg.logf("final persist: %v", err)
	}
	if !d.managedAPI {
		d.awaitAPI(apiDrainWindow)
	}
}

func (d *Daemon) initLedger() error {
	d.admissionStore = d.cfg.AdmissionStore
	if d.admissionStore == nil {
		d.admissionStore, _ = d.cfg.Runs.(MachineAdmissionStore)
	}
	var payload []byte
	if d.admissionStore != nil {
		var err error
		payload, err = d.admissionStore.LoadMachineAdmission(d.admissionContext)
		if err != nil {
			return fmt.Errorf("wingd: load machine admission from state.db: %w", err)
		}
	}
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
	if len(payload) > 0 {
		if err := d.restoreAdmissionPayloadLocked(payload); err != nil {
			return err
		}
		d.admissionCommitted = payload
		d.ledger.SetSchedulingPolicy(d.cfg.admissionPolicy().Scheduling)
		usedCores, usedMemory := d.ledger.Used()
		totalCores, totalMemory := max(d.budgetCores, usedCores), max(d.budgetMemory, usedMemory)
		for _, waiter := range d.ledger.Snapshot().Waiters {
			totalCores = max(totalCores, float64(waiter.MilliCores)/1000)
			totalMemory = max(totalMemory, waiter.MemoryBytes)
		}
		if err := d.ledger.ResizeTotals(totalCores, totalMemory); err != nil {
			return err
		}
	} else {
		lg, err := admission.New(admission.Config{TotalCores: d.budgetCores, TotalMemoryBytes: d.budgetMemory, Scheduling: d.cfg.admissionPolicy().Scheduling})
		if err != nil {
			return fmt.Errorf("wingd: new ledger: %w", err)
		}
		d.ledger = lg
	}
	if d.cfg.Runs != nil {
		checked := map[wingwire.RunID]bool{}
		for _, ticket := range d.admissionTickets {
			if !ticket.Active {
				continue
			}
			runID := ticket.Input.Request.RunID.RunID()
			if checked[runID] {
				continue
			}
			checked[runID] = true
			terminal, err := d.cfg.Runs.IsRunTerminal(runID)
			if err != nil {
				return fmt.Errorf("reconcile machine admission run %s: %w", runID, err)
			}
			if terminal {
				if err := d.cancelAdmissionRunLocked(string(runID), "run was cancelled or completed while controller was stopped"); err != nil {
					return err
				}
			}
		}
	}
	if err := d.reapAdmissionLocked(); err != nil {
		return err
	}
	if err := d.commitAdmissionLocked(); err != nil {
		return err
	}
	return nil
}

func (d *Daemon) now() time.Time { return d.cfg.now() }

func (d *Daemon) isDrainingLocked() bool { return d.draining }

func chargedResources(r wingwire.HostResources) wingwire.HostResources {
	if r.Cores == 0 && r.MemoryBytes == 0 {
		return wingwire.HostResources{Cores: admission.DefaultChargeCores}
	}
	return r
}

func (d *Daemon) clampHostChargeLocked(r wingwire.HostResources, costSource wingwire.CostSource) wingwire.HostResources {
	if costSource != wingwire.CostSourcePin {
		if maxCores := d.idleGrantableCoresLocked(); maxCores > 0 && r.Cores > maxCores {
			r.Cores = maxCores
		}
		if maxMem := d.idleGrantableMemoryLocked(); maxMem > 0 && r.MemoryBytes > int64(maxMem) {
			r.MemoryBytes = int64(maxMem)
		}
	}
	return r
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

func requestFromWireWithMetadata(runID, ownerRunID string, res wingwire.HostResources, sems []wingwire.SemaphoreClaim, costSource wingwire.CostSource, priority int, class string, expectedP99MS int64) admission.Request {
	req := admission.Request{
		ID:            runID,
		OwnerID:       ownerRunID,
		Priority:      priority,
		Class:         admission.WorkloadClass(class),
		ExpectedP99MS: expectedP99MS,
		Cores:         res.Cores,
		SoftCores:     softCoreCostSource(costSource),
		StrictCores:   strictCoreCostSource(costSource),
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

func (d *Daemon) runCancelledLocked(runID string) bool {
	if _, cancelled := d.cancelledRuns[runID]; cancelled {
		return true
	}
	if _, cancelled := d.cancelledRuns[string(wingwire.HolderRef(runID).RunID())]; cancelled {
		return true
	}
	return d.ledger.Orphaned(runID)
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

func (d *Daemon) beginDrain() (int, error) {
	remaining := d.markDraining()
	err := d.persistState()
	return remaining, err
}

func (d *Daemon) markDraining() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.draining = true
	return len(d.leaseRun)
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

func (d *Daemon) persistState() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.commitAdmissionLocked()
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

func (d *Daemon) runConnsLocked(runID string) []*conn {
	if runID == "" {
		return nil
	}
	var out []*conn
	if own := d.byRun[runID]; own != nil {
		out = append(out, own)
	}
	for c := range d.conns {
		if (c.ownerRunID != runID && string(wingwire.HolderRef(c.runID).RunID()) != runID) || c.runID == runID {
			continue
		}
		if c.role != roleWaiter && c.role != roleHolder {
			continue
		}
		out = append(out, c)
	}
	return out
}

// resolveRelativePriority answers the rank front or back means for runID: the
// launch-side rule in wingwire, applied to the waiters that are not this run's
// own participants, so asking for front twice is stable rather than a run
// chasing its own rank upward.
func resolveRelativePriority(snap admission.Snapshot, runID, mode string) (int, bool) {
	switch mode {
	case wingwire.PriorityFront, wingwire.PriorityBack:
	default:
		return 0, false
	}
	others := make([]wingwire.Waiter, 0, len(snap.Waiters))
	for _, w := range snap.Waiters {
		if w.RequestID == runID || w.OwnerID == runID || string(wingwire.HolderRef(w.RequestID).RunID()) == runID {
			continue
		}
		others = append(others, wingwire.Waiter{Priority: w.Priority})
	}
	return wingwire.ResolveRelativePriority(others, mode), true
}

// runQueuePosition reports the best 1-based queue position any participant of
// runID holds, or zero when none of them is waiting. It is the position the
// queue view prints, so an operator can compare the two.
func runQueuePosition(snap admission.Snapshot, runID string) int {
	for i, w := range snap.Waiters {
		if w.RequestID == runID || w.OwnerID == runID || string(wingwire.HolderRef(w.RequestID).RunID()) == runID {
			return i + 1
		}
	}
	return 0
}
