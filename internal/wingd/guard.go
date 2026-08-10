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

// SessionGuardInspector is the kernel boundary behind durable guarded
// admission. Every uncertain inspection returns an error so the daemon keeps
// the claim rather than promoting overlapping work.
type SessionGuardInspector interface {
	Validate(wingwire.ProcessSession) error
	Quiescent(wingwire.ProcessSession) (bool, error)
	Empty(wingwire.ProcessSession) (bool, error)
	Terminate(wingwire.ProcessSession) error
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
	session := guard.Session
	d.mu.Unlock()
	empty, err := d.guardInspector.Empty(session)
	if err != nil || !empty {
		if err != nil {
			d.cfg.logf("guard: completion inspection for %s: %v", guard.RunID, err)
		}
		return
	}
	deliveries, released, err := d.releaseGuardDurably(c.leaseID, session)
	if err != nil {
		d.cfg.logf("guard: persist completion for %s: %v", guard.RunID, err)
		c.close()
		return
	}
	if !released {
		c.close()
		return
	}
	for _, delivery := range deliveries {
		if err := delivery.c.send(delivery.msg); err != nil {
			go d.handleDisconnect(delivery.c)
		}
	}
	_ = c.send(&wingwire.GuardCompleteAck{})
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
	if d.cfg.FinalizeCancelledRuns != nil {
		if err := d.cfg.FinalizeCancelledRuns(append([]string(nil), affected...), reason); err != nil {
			d.cfg.logf("cancel: finalize runs %s: %v", strings.Join(affected, ","), err)
			d.mu.Lock()
			for _, runID := range affected {
				delete(d.cancelPending, runID)
			}
			d.mu.Unlock()
			c.close()
			return
		}
	} else if d.cfg.FinalizeRun != nil {
		for _, runID := range affected {
			d.cfg.FinalizeRun(runID)
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

func (d *Daemon) disconnectedGuardsLocked() []persistedGuard {
	guards := make([]persistedGuard, 0, len(d.guards))
	for _, guard := range d.guards {
		if guard.disconnected {
			guards = append(guards, guard.persistedGuard)
		}
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].LeaseID < guards[j].LeaseID })
	return guards
}

func (d *Daemon) guardLoop(ctxDone <-chan struct{}) {
	ticker := time.NewTicker(d.cfg.guardInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-d.quit:
			return
		case <-ticker.C:
			d.reconcileGuards()
		}
	}
}

func (d *Daemon) reconcileGuards() {
	d.mu.Lock()
	guards := d.disconnectedGuardsLocked()
	d.mu.Unlock()
	for _, guard := range guards {
		empty, err := d.guardInspector.Empty(guard.Session)
		if err != nil {
			d.cfg.logf("guard: inspect run %s: %v", guard.RunID, err)
			continue
		}
		if !empty {
			continue
		}
		d.releaseEmptyGuard(guard)
	}
}

func (d *Daemon) releaseEmptyGuard(guard persistedGuard) {
	deliveries, released, err := d.releaseGuardDurably(guard.LeaseID, guard.Session)
	if err != nil {
		d.cfg.logf("guard: persist empty session for %s: %v", guard.RunID, err)
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
	if d.cfg.FinalizeRun != nil {
		go d.cfg.FinalizeRun(guard.RunID)
	}
}

// releaseGuardDurably removes one guarded lease from durable state before
// exposing the resulting promotions in memory. The persistence mutex orders
// this transition with every other snapshot writer; the daemon mutex keeps
// admission from observing the preview until the write succeeds.
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
