// Package procgroup owns a child process tree until its exact Unix process
// group is empty and its leader can be reaped safely.
package procgroup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCleanup identifies a process group that could not be proven empty.
var ErrCleanup = errors.New("process group cleanup failed")

var sessionProcessTable = processTable

var sessionIdentityLookup = sessionIdentity

// DefaultTerminationGrace is the cooperative window every queue-exec owner
// gives a command after SIGTERM before escalating to SIGKILL.
const DefaultTerminationGrace = time.Second

const guardedSessionTerminateGrace = DefaultTerminationGrace

const guardedSessionTerminateTimeout = 5 * time.Second

const guardedSessionPollInterval = 10 * time.Millisecond

// guardedSessionMaxPollInterval caps the bounded termination poll. The
// wait it backs off is already limited by a deadline, so the cap only
// needs to keep a five-second wait from costing hundreds of process-table
// listings while still noticing an emptied session promptly.
const guardedSessionMaxPollInterval = 100 * time.Millisecond

// descendantMaxPollInterval caps the unbounded post-SIGKILL wait. A
// descendant that cannot be killed -- uninterruptible I/O, a stopped
// process -- makes that wait last as long as the caller's context, so its
// steady-state cost has to be about one process-table listing per second
// rather than a hundred.
const descendantMaxPollInterval = time.Second

// descendantEscalationInterval is the poll interval at which a wait is
// reported once as slow, so an operator reading the log learns that a
// process tree is refusing to die instead of only seeing the daemon busy.
const descendantEscalationInterval = 200 * time.Millisecond

// Info describes one process-table entry.
type Info struct {
	PID     int
	Group   int
	Session int
	State   string
}

// SessionIdentity binds a process session to the kernel creation identity of
// its leader so a reused numeric PID cannot inherit cleanup authority.
type SessionIdentity struct {
	LeaderPID  int
	SessionID  int
	BirthToken string
}

// Group retains an unreaped group leader as the stable ownership anchor for
// every signal and membership check.
type Group struct {
	cmd        *exec.Cmd
	id         int
	leaderDone chan struct{}
	leaderErr  error
	leaderMu   sync.Mutex
	finishMu   sync.Mutex
	reaped     bool
	reapedFlag atomic.Bool
	waitErr    error
	session    bool
	inspectMu  sync.Mutex
	inspect    func(int, bool, bool) (bool, error)
}

// Supported reports whether exact process-group ownership is available.
func Supported() error { return platformSupport() }

// GuardedSessionSupported reports whether the platform exposes a stable
// session-leader birth identity suitable for durable admission ownership.
func GuardedSessionSupported() error { return guardedSessionSupport() }

// CaptureSession returns the exact session identity rooted at pid.
func CaptureSession(pid int) (SessionIdentity, error) {
	if err := GuardedSessionSupported(); err != nil {
		return SessionIdentity{}, err
	}
	sid, token, err := sessionIdentity(pid)
	if err != nil {
		return SessionIdentity{}, err
	}
	if pid <= 1 || sid != pid || token == "" {
		return SessionIdentity{}, fmt.Errorf("process %d is not a stable session leader", pid)
	}
	return SessionIdentity{LeaderPID: pid, SessionID: sid, BirthToken: token}, nil
}

// SessionQuiescent reports whether no live process other than the registered
// leader remains in the session. Inspection errors are returned, never folded
// into an empty verdict.
func SessionQuiescent(identity SessionIdentity) (bool, error) {
	return inspectSession(identity, true)
}

// SessionEmpty reports whether the registered session contains no live
// process. Zombies do not execute and therefore do not retain admission.
func SessionEmpty(identity SessionIdentity) (bool, error) {
	return inspectSession(identity, false)
}

// TerminateSession signals every process group still belonging to the exact
// registered session after validating its leader identity.
func TerminateSession(identity SessionIdentity) error {
	empty, err := inspectSession(identity, false)
	if err != nil || empty {
		return err
	}
	if err := signalGuardSession(identity.SessionID, false); err != nil {
		return err
	}
	if empty, err := waitSessionEmpty(identity, guardedSessionTerminateGrace); err != nil || empty {
		return err
	}
	if err := signalGuardSession(identity.SessionID, true); err != nil {
		return err
	}
	empty, err = waitSessionEmpty(identity, guardedSessionTerminateTimeout)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("guarded session %d remained live after termination", identity.SessionID)
	}
	return nil
}

// backoffPoll yields a poll interval that doubles from a base up to a
// cap. Waiting for a process tree to disappear is cheap to start and
// unbounded in the worst case, so the interval that answers quickly when
// the wait is short must not be the interval a long wait keeps paying.
type backoffPoll struct {
	interval time.Duration
	max      time.Duration
}

func newBackoffPoll(base, max time.Duration) *backoffPoll {
	if base <= 0 {
		base = guardedSessionPollInterval
	}
	if max < base {
		max = base
	}
	return &backoffPoll{interval: base, max: max}
}

// next returns the interval to wait before the next poll and widens the
// one after it.
func (p *backoffPoll) next() time.Duration {
	current := p.interval
	if p.interval < p.max {
		p.interval *= 2
		if p.interval > p.max {
			p.interval = p.max
		}
	}
	return current
}

func waitSessionEmpty(identity SessionIdentity, timeout time.Duration) (bool, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := newBackoffPoll(guardedSessionPollInterval, guardedSessionMaxPollInterval)
	timer := time.NewTimer(poll.next())
	defer timer.Stop()
	for {
		empty, err := inspectSession(identity, false)
		if err != nil || empty {
			return empty, err
		}
		select {
		case <-deadline.C:
			return false, nil
		case <-timer.C:
			timer.Reset(poll.next())
		}
	}
}

// SessionTable is one process-table snapshot several guarded sessions can
// be judged against. A daemon watching many sessions at once pays one
// listing per sweep with it, where asking per session pays one listing --
// a `ps` fork and a syscall per live process -- for every session on
// every sweep.
type SessionTable struct {
	processes []Info
}

// CaptureSessionTable snapshots the process table with session
// identifiers populated.
func CaptureSessionTable() (*SessionTable, error) {
	processes, err := sessionProcessTable(true)
	if err != nil {
		return nil, err
	}
	return &SessionTable{processes: processes}, nil
}

// SessionEmpty answers [SessionEmpty] for identity against this snapshot,
// as of the moment the snapshot was taken.
func (t *SessionTable) SessionEmpty(identity SessionIdentity) (bool, error) {
	if t == nil {
		return false, fmt.Errorf("nil process session table")
	}
	return inspectSessionTable(t.processes, identity, false)
}

func inspectSession(identity SessionIdentity, excludeLeader bool) (bool, error) {
	if err := validateSessionIdentity(identity); err != nil {
		return false, err
	}
	processes, err := sessionProcessTable(true)
	if err != nil {
		return false, err
	}
	return inspectSessionTable(processes, identity, excludeLeader)
}

func validateSessionIdentity(identity SessionIdentity) error {
	if identity.LeaderPID <= 1 || identity.SessionID != identity.LeaderPID || identity.BirthToken == "" {
		return fmt.Errorf("invalid guarded session identity")
	}
	return nil
}

func inspectSessionTable(processes []Info, identity SessionIdentity, excludeLeader bool) (bool, error) {
	if err := validateSessionIdentity(identity); err != nil {
		return false, err
	}
	var leaderInSession bool
	for _, process := range processes {
		if process.PID == identity.LeaderPID && process.Session == identity.SessionID {
			leaderInSession = true
			break
		}
	}
	leaderReused := false
	if leaderInSession {
		_, token, err := sessionIdentityLookup(identity.LeaderPID)
		if err != nil {
			return false, err
		}
		if token != identity.BirthToken {
			leaderReused = true
		}
	}
	for _, process := range processes {
		if process.Session != identity.SessionID || processTerminated(process.State) {
			continue
		}
		if leaderReused && process.PID == identity.LeaderPID {
			continue
		}
		if leaderReused {
			return false, fmt.Errorf("guarded session %d has live members after leader identity reuse", identity.SessionID)
		}
		if excludeLeader && process.PID == identity.LeaderPID && leaderInSession {
			continue
		}
		return false, nil
	}
	return true, nil
}

func processTerminated(state string) bool {
	if state == "" {
		return false
	}
	switch state[0] {
	case 'Z', 'X', 'x':
		return true
	default:
		return false
	}
}

// Start launches cmd as the leader of a new owned process group.
func Start(cmd *exec.Cmd) (*Group, error) {
	return start(cmd, false)
}

// StartSession launches cmd as the leader of a new owned process session.
// Every nested process group remains inside that exact session.
func StartSession(cmd *exec.Cmd) (*Group, error) {
	return start(cmd, true)
}

func start(cmd *exec.Cmd, session bool) (*Group, error) {
	if err := platformSupport(); err != nil {
		return nil, err
	}
	if err := configure(cmd, session); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	g := &Group{
		cmd:        cmd,
		id:         cmd.Process.Pid,
		leaderDone: make(chan struct{}),
		session:    session,
		inspect:    descendantsEmpty,
	}
	go func() {
		err := waitLeaderExit(g.id)
		g.leaderMu.Lock()
		g.leaderErr = err
		g.leaderMu.Unlock()
		close(g.leaderDone)
	}()
	return g, nil
}

// ID returns the stable process-group identifier and leader PID.
func (g *Group) ID() int { return g.id }

// LeaderExited closes when the direct child has exited but remains unreaped.
func (g *Group) LeaderExited() <-chan struct{} { return g.leaderDone }

// Reaped reports whether the group was proven empty and its leader reaped.
func (g *Group) Reaped() bool {
	return g.reapedFlag.Load()
}

// SetDescendantProbe replaces the probe that decides whether the group's
// descendants are gone; a nil probe restores the kernel-backed one. Tests use
// it to force a cleanup failure deterministically rather than racing a
// deadline against a leader that may exit first.
func (g *Group) SetDescendantProbe(probe func(group int, exited, session bool) (bool, error)) {
	g.inspectMu.Lock()
	defer g.inspectMu.Unlock()
	if probe == nil {
		g.inspect = descendantsEmpty
		return
	}
	g.inspect = probe
}

func (g *Group) descendantProbe() func(int, bool, bool) (bool, error) {
	g.inspectMu.Lock()
	defer g.inspectMu.Unlock()
	return g.inspect
}

// Kill sends SIGKILL only while the original unreaped leader still anchors
// the exact group.
func (g *Group) Kill() error {
	g.finishMu.Lock()
	defer g.finishMu.Unlock()
	if g.reaped {
		return nil
	}
	return signalKill(g.id, g.leaderHasExited(), g.session)
}

// Finish waits for natural leader exit, empties descendants, and only then
// reaps the leader. A cleanup error retains ownership for a later retry.
func (g *Group) Finish(ctx context.Context, grace time.Duration) error {
	if err := g.awaitLeader(ctx); err != nil {
		return fmt.Errorf("%w: wait for group %d leader: %w", ErrCleanup, g.id, err)
	}
	return g.finish(ctx, grace)
}

// Terminate stops the exact group, proves descendants empty, and only then
// reaps the leader. A cleanup error retains ownership for a later retry.
func (g *Group) Terminate(ctx context.Context, grace time.Duration) error {
	g.finishMu.Lock()
	if g.reaped {
		err := g.waitErr
		g.finishMu.Unlock()
		return err
	}
	err := signalTerminate(g.id, g.leaderHasExited(), g.session)
	g.finishMu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: terminate group %d: %w", ErrCleanup, g.id, err)
	}

	graceCtx, cancel := boundedContext(ctx, grace)
	err = g.awaitLeader(graceCtx)
	cancel()
	if err != nil {
		g.finishMu.Lock()
		if g.reaped {
			result := g.waitErr
			g.finishMu.Unlock()
			return result
		}
		err = signalKill(g.id, g.leaderHasExited(), g.session)
		g.finishMu.Unlock()
		if err != nil {
			return fmt.Errorf("%w: kill group %d: %w", ErrCleanup, g.id, err)
		}
	}
	if err := g.awaitLeader(ctx); err != nil {
		return fmt.Errorf("%w: wait for killed group %d leader: %w", ErrCleanup, g.id, err)
	}
	return g.finish(ctx, grace)
}

func (g *Group) finish(ctx context.Context, grace time.Duration) error {
	g.finishMu.Lock()
	defer g.finishMu.Unlock()
	if g.reaped {
		return g.waitErr
	}
	if err := g.leaderExitError(); err != nil {
		return fmt.Errorf("%w: observe group %d leader: %w", ErrCleanup, g.id, err)
	}
	if err := g.emptyDescendants(ctx, grace); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanup, err)
	}
	g.waitErr = g.cmd.Wait()
	g.reaped = true
	g.reapedFlag.Store(true)
	return g.waitErr
}

func (g *Group) awaitLeader(ctx context.Context) error {
	select {
	case <-g.leaderDone:
		return g.leaderExitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Group) leaderExitError() error {
	g.leaderMu.Lock()
	defer g.leaderMu.Unlock()
	return g.leaderErr
}

func (g *Group) leaderHasExited() bool {
	select {
	case <-g.leaderDone:
		return true
	default:
		return false
	}
}

func boundedContext(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 100 * time.Millisecond
	}
	return context.WithTimeout(parent, d)
}

func (g *Group) emptyDescendants(ctx context.Context, grace time.Duration) error {
	empty, err := g.descendantProbe()(g.id, true, g.session)
	if err != nil || empty {
		return err
	}
	if err := signalTerminate(g.id, true, g.session); err != nil {
		return err
	}
	graceCtx, cancel := boundedContext(ctx, grace)
	err = g.waitDescendantsEmpty(graceCtx)
	cancel()
	if err == nil {
		return nil
	}
	if err := signalKill(g.id, true, g.session); err != nil {
		return err
	}
	return g.waitDescendantsEmpty(ctx)
}

func (g *Group) waitDescendantsEmpty(ctx context.Context) error {
	poll := newBackoffPoll(guardedSessionPollInterval, descendantMaxPollInterval)
	timer := time.NewTimer(poll.next())
	defer timer.Stop()
	reported := false
	for {
		empty, err := g.descendantProbe()(g.id, true, g.session)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("group %d descendants remained: %w", g.id, ctx.Err())
		case <-timer.C:
			next := poll.next()
			if !reported && next >= descendantEscalationInterval {
				reported = true
				slog.Warn("process group descendants still live; slowing the wait",
					"group", g.id, "poll_interval", next.String())
			}
			timer.Reset(next)
		}
	}
}

// List returns the current process table for owned-group accounting.
func List() ([]Info, error) { return processTable(false) }

// ListSessions returns the process table with session identifiers populated.
func ListSessions() ([]Info, error) { return processTable(true) }

// IgnoreTermination makes a test helper ignore Unix group termination.
func IgnoreTermination() { ignoreTermination() }
