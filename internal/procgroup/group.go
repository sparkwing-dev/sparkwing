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

var ErrCleanup = errors.New("process group cleanup failed")

var ErrProcessAbsent = errors.New("process is absent")

var sessionProcessTable = processTable

var sessionIdentityLookup = sessionIdentity

const DefaultTerminationGrace = time.Second

const guardedSessionTerminateGrace = DefaultTerminationGrace

const guardedSessionTerminateTimeout = 5 * time.Second

const guardedSessionPollInterval = 10 * time.Millisecond

const guardedSessionMaxPollInterval = 100 * time.Millisecond

const descendantMaxPollInterval = time.Second

const descendantEscalationInterval = 200 * time.Millisecond

type Info struct {
	PID     int
	Group   int
	Session int
	State   string

	Birth string
}

type SessionIdentity struct {
	LeaderPID  int
	SessionID  int
	BirthToken string
}

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

func Supported() error { return platformSupport() }

func GuardedSessionSupported() error { return guardedSessionSupport() }

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

func SessionQuiescent(identity SessionIdentity) (bool, error) {
	return inspectSession(identity, true)
}

func SessionEmpty(identity SessionIdentity) (bool, error) {
	return inspectSession(identity, false)
}

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

type SessionTable struct {
	processes []Info
}

func CaptureSessionTable() (*SessionTable, error) {
	processes, err := sessionProcessTable(true)
	if err != nil {
		return nil, err
	}
	return &SessionTable{processes: processes}, nil
}

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
	var leaderBirth string
	for _, process := range processes {
		if process.PID == identity.LeaderPID && process.Session == identity.SessionID {
			leaderInSession = true
			leaderBirth = process.Birth
			break
		}
	}
	leaderReused := false
	if leaderInSession {
		token := leaderBirth
		if token == "" {
			var err error
			_, token, err = sessionIdentityLookup(identity.LeaderPID)
			if errors.Is(err, ErrProcessAbsent) {
				// safety: leader exit between snapshot and lookup is an empty-session
				// observation, not an inspection failure that callers should retry.
				leaderInSession = false
			} else if err != nil {
				return false, err
			}
		}
		if leaderInSession && token != identity.BirthToken {
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

func Start(cmd *exec.Cmd) (*Group, error) {
	return start(cmd, false)
}

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

func (g *Group) ID() int { return g.id }

func (g *Group) LeaderExited() <-chan struct{} { return g.leaderDone }

func (g *Group) Reaped() bool {
	return g.reapedFlag.Load()
}

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

func (g *Group) Kill() error {
	g.finishMu.Lock()
	defer g.finishMu.Unlock()
	if g.reaped {
		return nil
	}
	return signalKill(g.id, g.leaderHasExited(), g.session)
}

func (g *Group) Finish(ctx context.Context, grace time.Duration) error {
	if err := g.awaitLeader(ctx); err != nil {
		return fmt.Errorf("%w: wait for group %d leader: %w", ErrCleanup, g.id, err)
	}
	return g.finish(ctx, grace)
}

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

func List() ([]Info, error) { return processTable(false) }

func ListSessions() ([]Info, error) { return processTable(true) }

func IgnoreTermination() { ignoreTermination() }
