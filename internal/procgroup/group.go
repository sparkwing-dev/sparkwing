// Package procgroup owns a child process tree until its exact Unix process
// group is empty and its leader can be reaped safely.
package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// ErrCleanup identifies a process group that could not be proven empty.
var ErrCleanup = errors.New("process group cleanup failed")

var sessionProcessTable = processTable

var sessionIdentityLookup = sessionIdentity

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
	return terminateGuardSession(identity.SessionID)
}

func inspectSession(identity SessionIdentity, excludeLeader bool) (bool, error) {
	if identity.LeaderPID <= 1 || identity.SessionID != identity.LeaderPID || identity.BirthToken == "" {
		return false, fmt.Errorf("invalid guarded session identity")
	}
	processes, err := sessionProcessTable(true)
	if err != nil {
		return false, err
	}
	var leaderInSession bool
	for _, process := range processes {
		if process.PID == identity.LeaderPID && process.Session == identity.SessionID {
			leaderInSession = true
			break
		}
	}
	if leaderInSession {
		_, token, err := sessionIdentityLookup(identity.LeaderPID)
		if err != nil {
			return false, err
		}
		if token != identity.BirthToken {
			return false, fmt.Errorf("guarded session leader %d birth identity changed", identity.LeaderPID)
		}
	}
	for _, process := range processes {
		if process.Session != identity.SessionID || processTerminated(process.State) {
			continue
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
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
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
		case <-ticker.C:
		}
	}
}

// List returns the current process table for owned-group accounting.
func List() ([]Info, error) { return processTable(false) }

// ListSessions returns the process table with session identifiers populated.
func ListSessions() ([]Info, error) { return processTable(true) }

// IgnoreTermination makes a test helper ignore Unix group termination.
func IgnoreTermination() { ignoreTermination() }
