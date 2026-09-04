package wingd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const defaultGuardInterval = 100 * time.Millisecond

const maxGuardInterval = 5 * time.Second

type SessionGuardInspector interface {
	Validate(wingwire.ProcessSession) error
	Quiescent(wingwire.ProcessSession) (bool, error)
	Empty(wingwire.ProcessSession) (bool, error)
	Terminate(wingwire.ProcessSession) error
}

type SessionGuardSnapshotInspector interface {
	EmptySnapshot() (func(wingwire.ProcessSession) (bool, error), error)
}

type processSessionInspector struct{}

func (processSessionInspector) Validate(session wingwire.ProcessSession) error {
	identity, err := procgroup.CaptureSession(session.LeaderPID)
	if err != nil {
		return err
	}
	if identity.SessionID != session.SessionID || identity.BirthToken != session.BirthToken {
		return fmt.Errorf("guarded session identity does not match process %d", session.LeaderPID)
	}
	return nil
}

func (processSessionInspector) Quiescent(session wingwire.ProcessSession) (bool, error) {
	return procgroup.SessionQuiescent(procSessionIdentity(session))
}

func (processSessionInspector) Empty(session wingwire.ProcessSession) (bool, error) {
	return procgroup.SessionEmpty(procSessionIdentity(session))
}

func (processSessionInspector) EmptySnapshot() (func(wingwire.ProcessSession) (bool, error), error) {
	table, err := procgroup.CaptureSessionTable()
	if err != nil {
		return nil, err
	}
	return func(session wingwire.ProcessSession) (bool, error) {
		return table.SessionEmpty(procSessionIdentity(session))
	}, nil
}

func (processSessionInspector) Terminate(session wingwire.ProcessSession) error {
	return procgroup.TerminateSession(procSessionIdentity(session))
}

func procSessionIdentity(session wingwire.ProcessSession) procgroup.SessionIdentity {
	return procgroup.SessionIdentity{
		LeaderPID: session.LeaderPID, SessionID: session.SessionID, BirthToken: session.BirthToken,
	}
}

type sessionGuardState struct {
	persistedGuard
	disconnected bool
	terminating  bool
	completion   *conn
	graceTimer   *time.Timer
}

type guardReconcileState struct {
	persistedGuard
	completion *conn
	finalize   bool
}

func processSessionMatches(got, want *wingwire.ProcessSession) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func validGuardSession(session wingwire.ProcessSession) bool {
	return session.LeaderPID > 1 && session.SessionID == session.LeaderPID && session.BirthToken != ""
}

func persistedGuardForLease(guards []persistedGuard, leaseID admission.LeaseID) (persistedGuard, bool) {
	for _, guard := range guards {
		if guard.LeaseID == leaseID {
			return guard, true
		}
	}
	return persistedGuard{}, false
}

func (d *Daemon) handleGuardComplete(c *conn, req *wingwire.GuardComplete) {
	d.mu.Lock()
	guard := d.guards[c.leaseID]
	if c.role != roleHolder || guard == nil {
		d.mu.Unlock()
		c.close()
		return
	}
	lease, ok := d.ledger.LeaseByID(c.leaseID)
	if !ok || lease.Token != req.LeaseToken {
		d.mu.Unlock()
		c.close()
		return
	}
	guard.completion = c
	session := guard.Session
	d.mu.Unlock()
	empty, err := d.guardInspector.Empty(session)
	if err != nil || !empty {
		if err != nil {
			d.cfg.logf("guard: completion inspection for %s: %v", guard.RunID, err)
		}
		return
	}
	d.completeEmptyGuard(guardReconcileState{persistedGuard: guard.persistedGuard, completion: c})
}

func (d *Daemon) completeEmptyGuard(guard guardReconcileState) {
	deliveries, released, err := d.releaseGuardDurably(guard.LeaseID, guard.Session)
	if err != nil {
		d.cfg.logf("guard: persist completion for %s: %v", guard.RunID, err)
		if guard.completion != nil {
			guard.completion.close()
		}
		return
	}
	if !released {
		return
	}
	for _, delivery := range deliveries {
		if err := delivery.c.send(delivery.msg); err != nil {
			go d.handleDisconnect(delivery.c)
		}
	}
	if guard.completion != nil {
		_ = guard.completion.send(&wingwire.GuardCompleteAck{})
	}
	if guard.finalize {
		d.finalizeAsync(guard.RunID)
	}
}

// safety: a guarded tree outlives its client on purpose, but nothing else ever
// reaps it -- the sweep only watches for it to end -- so a client that never
// comes back would hold its charge, and the daemon's idle exit, forever.
func (d *Daemon) armGuardGraceLocked(id admission.LeaseID, guard *sessionGuardState) {
	if guard.graceTimer != nil {
		return
	}
	session := guard.Session
	guard.graceTimer = time.AfterFunc(d.cfg.graceWindow(), func() { d.expireGuardGrace(id, session) })
}

func (d *Daemon) stopGuardGraceLocked(guard *sessionGuardState) {
	if guard.graceTimer == nil {
		return
	}
	guard.graceTimer.Stop()
	guard.graceTimer = nil
}

func (d *Daemon) expireGuardGrace(id admission.LeaseID, session wingwire.ProcessSession) {
	d.mu.Lock()
	guard := d.guards[id]
	if d.shuttingDown || guard == nil || guard.Session != session || !guard.disconnected {
		if guard != nil && guard.Session == session {
			guard.graceTimer = nil
		}
		d.mu.Unlock()
		return
	}
	guard.graceTimer = nil
	// safety: Terminate blocks for the session's own shutdown, and a client that
	// reattached in that window would be handed a lease over an already dead
	// process tree, so reattach is refused from here until the outcome is known.
	guard.terminating = true
	reclaim := guardReconcileState{persistedGuard: guard.persistedGuard, completion: guard.completion, finalize: true}
	d.mu.Unlock()

	d.cfg.logf("guard: run %s lost its client %s ago; terminating the guarded session",
		reclaim.RunID, d.cfg.graceWindow())
	if err := d.guardInspector.Terminate(session); err != nil {
		d.cfg.logf("guard: terminate abandoned session for %s: %v", reclaim.RunID, err)
		d.mu.Lock()
		if current := d.guards[id]; current != nil && current.Session == session {
			current.terminating = false
			if current.disconnected {
				d.armGuardGraceLocked(id, current)
			}
		}
		d.mu.Unlock()
		return
	}
	d.completeEmptyGuard(reclaim)
}

func (d *Daemon) disconnectedGuardForRunLocked(runID string) *sessionGuardState {
	for _, guard := range d.guards {
		if guard.disconnected && guard.RunID == runID {
			return guard
		}
	}
	return nil
}

func (d *Daemon) cancelDisconnectedGuard(c *conn, guard persistedGuard, affected []string) {
	const reason = "cancelled via sparkwing runs cancel"
	if len(affected) == 0 {
		d.mu.Lock()
		delete(d.cancelPending, guard.RunID)
		d.mu.Unlock()
		c.close()
		return
	}
	if d.cfg.Runs != nil {
		if err := d.cfg.Runs.FinalizeCancelledRuns(append([]string(nil), affected...), reason); err != nil {
			d.cfg.logf("cancel: finalize runs %s: %v", strings.Join(affected, ","), err)
			d.mu.Lock()
			for _, runID := range affected {
				delete(d.cancelPending, runID)
			}
			d.mu.Unlock()
			c.close()
			return
		}
	}

	d.mu.Lock()
	current := d.guards[guard.LeaseID]
	if current == nil || current.Session != guard.Session {
		for _, runID := range affected {
			delete(d.cancelPending, runID)
		}
		d.mu.Unlock()
		c.close()
		return
	}
	for _, runID := range affected {
		delete(d.cancelPending, runID)
		d.recordCancelledRunLocked(runID)
	}
	snap := d.ledger.Snapshot()
	d.touchLocked()
	d.mu.Unlock()
	if err := d.persistState(snap); err != nil {
		d.cfg.logf("cancel: persist disconnected guard: %v", err)
		c.close()
		return
	}
	if err := d.guardInspector.Terminate(guard.Session); err != nil {
		d.cfg.logf("cancel: terminate disconnected guarded session %s: %v", guard.RunID, err)
		c.close()
		return
	}
	_ = c.send(&wingwire.CancelLeaseAck{Found: true})
}

func (d *Daemon) persistedGuardsLocked() []persistedGuard {
	guards := make([]persistedGuard, 0, len(d.guards))
	for _, guard := range d.guards {
		guards = append(guards, guard.persistedGuard)
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].LeaseID < guards[j].LeaseID })
	return guards
}

func (d *Daemon) reconcilableGuardsLocked() []guardReconcileState {
	guards := make([]guardReconcileState, 0, len(d.guards))
	for _, guard := range d.guards {
		if guard.disconnected || guard.completion != nil {
			guards = append(guards, guardReconcileState{
				persistedGuard: guard.persistedGuard, completion: guard.completion, finalize: guard.disconnected,
			})
		}
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].LeaseID < guards[j].LeaseID })
	return guards
}

func (d *Daemon) guardLoop(ctxDone <-chan struct{}) {
	base := d.cfg.guardInterval()
	delay := base
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-d.quit:
			return
		case <-timer.C:
			if err := d.reconcileGuards(); err != nil {
				delay = nextGuardDelay(delay, base)
				d.cfg.logf("guard: %v; next sweep in %s", err, delay)
			} else {
				delay = base
			}
			timer.Reset(delay)
		}
	}
}

func nextGuardDelay(current, base time.Duration) time.Duration {
	limit := maxGuardInterval
	if base > limit {
		limit = base
	}
	next := current * 2
	if next < base {
		next = base
	}
	if next > limit {
		next = limit
	}
	return next
}

func (d *Daemon) reconcileGuards() error {
	d.mu.Lock()
	guards := d.reconcilableGuardsLocked()
	d.mu.Unlock()
	if len(guards) == 0 {
		return nil
	}
	sessionEmpty, err := d.guardEmptyProbe()
	if err != nil {
		return fmt.Errorf("snapshot guarded sessions: %w", err)
	}
	var failure error
	failed := 0
	for _, guard := range guards {
		empty, err := sessionEmpty(guard.Session)
		if err != nil {
			failed++
			if failure == nil {
				failure = fmt.Errorf("inspect run %s: %w", guard.RunID, err)
			}
			continue
		}
		if !empty {
			continue
		}
		d.completeEmptyGuard(guard)
	}
	if failed == len(guards) {
		return failure
	}
	if failure != nil {
		d.cfg.logf("guard: %v", failure)
	}
	return nil
}

func (d *Daemon) guardEmptyProbe() (func(wingwire.ProcessSession) (bool, error), error) {
	if snapshotter, ok := d.guardInspector.(SessionGuardSnapshotInspector); ok {
		return snapshotter.EmptySnapshot()
	}
	return d.guardInspector.Empty, nil
}

func (d *Daemon) releaseGuardDurably(leaseID admission.LeaseID, session wingwire.ProcessSession) ([]delivery, bool, error) {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	current := d.guards[leaseID]
	if current == nil || current.Session != session {
		return nil, false, nil
	}
	previous := d.ledger.Snapshot()
	members := guardLeaseMembers(previous, leaseID)
	if len(members) == 0 {
		return nil, false, fmt.Errorf("guarded lease %s has no members", leaseID)
	}
	var events []admission.Event
	for _, member := range members {
		released, err := d.ledger.Release(leaseID, member)
		if err != nil {
			d.restoreGuardedTransition(previous)
			return nil, false, fmt.Errorf("apply guarded release %s: %w", leaseID, err)
		}
		events = append(events, released...)
	}
	next := d.ledger.Snapshot()
	guards := d.persistedGuardsLocked()
	for i := range guards {
		if guards[i].LeaseID == leaseID {
			guards = append(guards[:i], guards[i+1:]...)
			break
		}
	}
	cancelled := append([]string(nil), d.cancelledRunOrder...)
	var writeErr error
	if d.persistWrite != nil {
		writeErr = d.persistWrite(d.layout.state, next, d.events.snapshot(d.now()), cancelled, guards)
	} else {
		writeErr = writeStateWithGuards(d.layout.state, next, d.events.snapshot(d.now()), cancelled, guards)
	}
	if writeErr != nil {
		d.restoreGuardedTransition(previous)
		return nil, false, writeErr
	}

	d.stopGuardGraceLocked(current)
	delete(d.guards, leaseID)
	deliveries := d.routeLocked(events)
	d.persistedEventSeq = next.EventSeq
	d.touchLocked()
	return deliveries, true, nil
}

func (d *Daemon) restoreGuardedTransition(snapshot admission.Snapshot) {
	restored, err := admission.Restore(snapshot, nil)
	if err != nil {
		panic(fmt.Sprintf("wingd: rollback guarded transition: %v", err))
	}
	d.ledger = restored
}

func guardLeaseMembers(snap admission.Snapshot, leaseID admission.LeaseID) []string {
	for _, lease := range snap.Leases {
		if lease.ID == leaseID {
			return append([]string(nil), lease.Members...)
		}
	}
	return nil
}
