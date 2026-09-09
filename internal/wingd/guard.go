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

type guardRelease struct {
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

func (daemon *Daemon) handleGuardComplete(connection *conn, request *wingwire.GuardComplete) {
	daemon.mu.Lock()
	guard := daemon.guards[connection.leaseID]
	if connection.role != roleHolder || guard == nil {
		daemon.mu.Unlock()
		connection.close()
		return
	}
	lease, ok := daemon.ledger.LeaseByID(connection.leaseID)
	if !ok || lease.Token != request.LeaseToken {
		daemon.mu.Unlock()
		connection.close()
		return
	}
	guard.completion = connection
	session := guard.Session
	daemon.mu.Unlock()
	empty, err := daemon.guardInspector.Empty(session)
	if err != nil || !empty {
		if err != nil {
			daemon.cfg.logf("guard: completion inspection for %s: %v", guard.RunID, err)
		}
		return
	}
	daemon.completeEmptyGuard(guard.persistedGuard)
}

func (daemon *Daemon) completeEmptyGuard(guard persistedGuard) {
	deliveries, release, err := daemon.releaseGuardDurably(guard.LeaseID, guard.Session)
	if err != nil {
		daemon.cfg.logf("guard: persist completion for %s: %v", guard.RunID, err)
		if release != nil && release.completion != nil {
			release.completion.close()
		}
		return
	}
	if release == nil {
		return
	}
	for _, delivery := range deliveries {
		if err := delivery.c.send(delivery.msg); err != nil {
			go daemon.handleDisconnect(delivery.c)
		}
	}
	if release.completion != nil {
		if err := release.completion.send(&wingwire.GuardCompleteAck{}); err != nil {
			daemon.cfg.logf("guard: acknowledge completion for %s: %v", guard.RunID, err)
			release.completion.close()
		}
	}
	if release.finalize {
		daemon.finalizeAsync(guard.RunID)
	}
}

func (daemon *Daemon) armGuardGraceLocked(leaseID admission.LeaseID, guard *sessionGuardState) {
	if guard.graceTimer != nil {
		return
	}
	session := guard.Session
	guard.graceTimer = time.AfterFunc(daemon.cfg.graceWindow(), func() { daemon.expireGuardGrace(leaseID, session) })
}

func (daemon *Daemon) stopGuardGraceLocked(guard *sessionGuardState) {
	if guard.graceTimer == nil {
		return
	}
	guard.graceTimer.Stop()
	guard.graceTimer = nil
}

func (daemon *Daemon) expireGuardGrace(leaseID admission.LeaseID, session wingwire.ProcessSession) {
	daemon.mu.Lock()
	guard := daemon.guards[leaseID]
	if daemon.shuttingDown || guard == nil || guard.Session != session || !guard.disconnected {
		if guard != nil && guard.Session == session {
			guard.graceTimer = nil
		}
		daemon.mu.Unlock()
		return
	}
	guard.graceTimer = nil
	// SAFETY: Refuse reattachment while termination can destroy the guarded session.
	guard.terminating = true
	reclaim := guard.persistedGuard
	daemon.mu.Unlock()

	daemon.cfg.logf("guard: run %s lost its client %s ago; terminating the guarded session",
		reclaim.RunID, daemon.cfg.graceWindow())
	if err := daemon.guardInspector.Terminate(session); err != nil {
		daemon.cfg.logf("guard: terminate abandoned session for %s: %v", reclaim.RunID, err)
		daemon.mu.Lock()
		if current := daemon.guards[leaseID]; current != nil && current.Session == session {
			current.terminating = false
			if current.disconnected {
				daemon.armGuardGraceLocked(leaseID, current)
			}
		}
		daemon.mu.Unlock()
		return
	}
	daemon.completeEmptyGuard(reclaim)
}

func (daemon *Daemon) disconnectedGuardForRunLocked(runID string) *sessionGuardState {
	for _, guard := range daemon.guards {
		if guard.disconnected && guard.RunID == runID {
			return guard
		}
	}
	return nil
}

func (daemon *Daemon) cancelDisconnectedGuard(connection *conn, guard persistedGuard, affected []string) {
	const reason = "cancelled via sparkwing runs cancel"
	if len(affected) == 0 {
		daemon.mu.Lock()
		delete(daemon.cancelPending, guard.RunID)
		daemon.mu.Unlock()
		connection.close()
		return
	}
	if daemon.cfg.Runs != nil {
		if err := daemon.cfg.Runs.FinalizeCancelledRuns(append([]string(nil), affected...), reason); err != nil {
			daemon.cfg.logf("cancel: finalize runs %s: %v", strings.Join(affected, ","), err)
			daemon.mu.Lock()
			for _, runID := range affected {
				delete(daemon.cancelPending, runID)
			}
			daemon.mu.Unlock()
			connection.close()
			return
		}
	}

	daemon.mu.Lock()
	current := daemon.guards[guard.LeaseID]
	if current == nil || current.Session != guard.Session {
		for _, runID := range affected {
			delete(daemon.cancelPending, runID)
		}
		daemon.mu.Unlock()
		connection.close()
		return
	}
	for _, runID := range affected {
		delete(daemon.cancelPending, runID)
		daemon.recordCancelledRunLocked(runID)
	}
	snapshot := daemon.ledger.Snapshot()
	daemon.touchLocked()
	daemon.mu.Unlock()
	if err := daemon.persistState(snapshot); err != nil {
		daemon.cfg.logf("cancel: persist disconnected guard: %v", err)
		connection.close()
		return
	}
	if err := daemon.guardInspector.Terminate(guard.Session); err != nil {
		daemon.cfg.logf("cancel: terminate disconnected guarded session %s: %v", guard.RunID, err)
		connection.close()
		return
	}
	if err := connection.send(&wingwire.CancelLeaseAck{Found: true}); err != nil {
		daemon.cfg.logf("cancel: acknowledge guarded run %s: %v", guard.RunID, err)
		connection.close()
	}
}

func (daemon *Daemon) persistedGuardsLocked() []persistedGuard {
	guards := make([]persistedGuard, 0, len(daemon.guards))
	for _, guard := range daemon.guards {
		guards = append(guards, guard.persistedGuard)
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].LeaseID < guards[j].LeaseID })
	return guards
}

func (daemon *Daemon) reconcilableGuardsLocked() []persistedGuard {
	guards := make([]persistedGuard, 0, len(daemon.guards))
	for _, guard := range daemon.guards {
		if guard.disconnected || guard.completion != nil {
			guards = append(guards, guard.persistedGuard)
		}
	}
	sort.Slice(guards, func(i, j int) bool { return guards[i].LeaseID < guards[j].LeaseID })
	return guards
}

func (daemon *Daemon) guardLoop(contextDone <-chan struct{}) {
	base := daemon.cfg.guardInterval()
	delay := base
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-contextDone:
			return
		case <-daemon.quit:
			return
		case <-timer.C:
			if err := daemon.reconcileGuards(); err != nil {
				delay = nextGuardDelay(delay, base)
				daemon.cfg.logf("guard: %v; next sweep in %s", err, delay)
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

func (daemon *Daemon) reconcileGuards() error {
	daemon.mu.Lock()
	guards := daemon.reconcilableGuardsLocked()
	daemon.mu.Unlock()
	if len(guards) == 0 {
		return nil
	}
	sessionEmpty, err := daemon.guardEmptyProbe()
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
		daemon.completeEmptyGuard(guard)
	}
	if failed == len(guards) {
		return failure
	}
	if failure != nil {
		daemon.cfg.logf("guard: %v", failure)
	}
	return nil
}

func (daemon *Daemon) guardEmptyProbe() (func(wingwire.ProcessSession) (bool, error), error) {
	if snapshotter, ok := daemon.guardInspector.(SessionGuardSnapshotInspector); ok {
		return snapshotter.EmptySnapshot()
	}
	return daemon.guardInspector.Empty, nil
}

// releaseGuardDurably returns the current completion recipient even on persistence failure.
func (daemon *Daemon) releaseGuardDurably(leaseID admission.LeaseID, session wingwire.ProcessSession) ([]delivery, *guardRelease, error) {
	daemon.persistMu.Lock()
	defer daemon.persistMu.Unlock()
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	current := daemon.guards[leaseID]
	if current == nil || current.Session != session {
		return nil, nil, nil
	}
	// SAFETY: Reattachment can change the recipient and ownership while session inspection runs.
	release := &guardRelease{completion: current.completion, finalize: current.disconnected}
	previous := daemon.ledger.Snapshot()
	members := guardLeaseMembers(previous, leaseID)
	if len(members) == 0 {
		return nil, release, fmt.Errorf("guarded lease %s has no members", leaseID)
	}
	var events []admission.Event
	for _, member := range members {
		released, err := daemon.ledger.Release(leaseID, member)
		if err != nil {
			daemon.restoreGuardedTransition(previous)
			return nil, release, fmt.Errorf("apply guarded release %s: %w", leaseID, err)
		}
		events = append(events, released...)
	}
	next := daemon.ledger.Snapshot()
	guards := daemon.persistedGuardsLocked()
	for i := range guards {
		if guards[i].LeaseID == leaseID {
			guards = append(guards[:i], guards[i+1:]...)
			break
		}
	}
	cancelled := append([]string(nil), daemon.cancelledRunOrder...)
	var writeErr error
	if daemon.persistWrite != nil {
		writeErr = daemon.persistWrite(daemon.layout.state, next, daemon.events.snapshot(daemon.now()), cancelled, guards)
	} else {
		writeErr = writeStateWithGuards(daemon.layout.state, next, daemon.events.snapshot(daemon.now()), cancelled, guards)
	}
	if writeErr != nil {
		daemon.restoreGuardedTransition(previous)
		return nil, release, writeErr
	}

	daemon.stopGuardGraceLocked(current)
	delete(daemon.guards, leaseID)
	deliveries := daemon.routeLocked(events)
	daemon.persistedEventSeq = next.EventSeq
	daemon.touchLocked()
	return deliveries, release, nil
}

func (daemon *Daemon) restoreGuardedTransition(snapshot admission.Snapshot) {
	restored, err := admission.Restore(snapshot, nil)
	if err != nil {
		panic(fmt.Sprintf("wingd: rollback guarded transition: %v", err))
	}
	daemon.ledger = restored
}

func guardLeaseMembers(snapshot admission.Snapshot, leaseID admission.LeaseID) []string {
	for _, lease := range snapshot.Leases {
		if lease.ID == leaseID {
			return append([]string(nil), lease.Members...)
		}
	}
	return nil
}
