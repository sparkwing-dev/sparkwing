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

const processTableTimeout = 2 * time.Second

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
	Exiting bool

	Birth string
}

type SessionIdentity struct {
	LeaderPID  int
	SessionID  int
	BirthToken string
}

type Group struct {
	command    *exec.Cmd
	id         int
	leaderDone chan struct{}
	leaderErr  error
	leaderMu   sync.Mutex
	cleanup    chan struct{}
	finishMu   sync.Mutex
	reaped     bool
	reapedFlag atomic.Bool
	waitErr    error
	session    bool
	inspectMu  sync.Mutex
	inspect    func(context.Context, int, bool, bool) (bool, error)
}

func Supported() error { return platformSupport() }

func GuardedSessionSupported() error { return guardedSessionSupport() }

func CaptureSession(pid int) (SessionIdentity, error) {
	if err := GuardedSessionSupported(); err != nil {
		return SessionIdentity{}, err
	}
	sessionID, token, err := sessionIdentity(pid)
	if err != nil {
		return SessionIdentity{}, err
	}
	if pid <= 1 || sessionID != pid || token == "" {
		return SessionIdentity{}, fmt.Errorf("process %d is not a stable session leader", pid)
	}
	return SessionIdentity{LeaderPID: pid, SessionID: sessionID, BirthToken: token}, nil
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

func DiagnosticSession(identity SessionIdentity) error {
	empty, err := inspectSession(identity, false)
	if err != nil || empty {
		return err
	}
	return signalDiagnosticSession(identity.SessionID)
}

func KillSession(identity SessionIdentity) error {
	empty, err := inspectSession(identity, false)
	if err != nil || empty {
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
		return fmt.Errorf("guarded session %d remained live after kill", identity.SessionID)
	}
	return nil
}

type backoffPoll struct {
	interval time.Duration
	max      time.Duration
}

func newBackoffPoll(base, max time.Duration) *backoffPoll {
	return &backoffPoll{interval: base, max: max}
}

func (poll *backoffPoll) next() time.Duration {
	current := poll.interval
	if poll.interval < poll.max {
		poll.interval *= 2
		if poll.interval > poll.max {
			poll.interval = poll.max
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
	processes, err := sessionProcessTable(context.Background(), true)
	if err != nil {
		return nil, err
	}
	return &SessionTable{processes: processes}, nil
}

func (table *SessionTable) SessionEmpty(identity SessionIdentity) (bool, error) {
	if table == nil {
		return false, fmt.Errorf("nil process session table")
	}
	return inspectSessionTable(table.processes, identity, false)
}

func inspectSession(identity SessionIdentity, excludeLeader bool) (bool, error) {
	if err := validateSessionIdentity(identity); err != nil {
		return false, err
	}
	processes, err := sessionProcessTable(context.Background(), true)
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
				// SAFETY: Leader disappearance between snapshot and lookup is a valid observation.
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

func Start(command *exec.Cmd) (*Group, error) {
	return start(command, false)
}

func StartSession(command *exec.Cmd) (*Group, error) {
	return start(command, true)
}

func start(command *exec.Cmd, session bool) (*Group, error) {
	if err := platformSupport(); err != nil {
		return nil, err
	}
	if err := configure(command, session); err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	group := &Group{
		command:    command,
		id:         command.Process.Pid,
		leaderDone: make(chan struct{}),
		cleanup:    make(chan struct{}, 1),
		session:    session,
		inspect:    descendantsEmpty,
	}
	go func() {
		err := waitLeaderExit(group.id)
		group.leaderMu.Lock()
		group.leaderErr = err
		group.leaderMu.Unlock()
		close(group.leaderDone)
	}()
	return group, nil
}

func (group *Group) ID() int { return group.id }

// LeaderExited closes when observation completes, including observation failure.
func (group *Group) LeaderExited() <-chan struct{} { return group.leaderDone }

// WaitLeaderExit waits for the leader observer to finish and returns its result.
func (group *Group) WaitLeaderExit() error {
	<-group.leaderDone
	return group.leaderExitError()
}

func (group *Group) Reaped() bool {
	return group.reapedFlag.Load()
}

func (group *Group) SetDescendantProbe(probe func(context.Context, int, bool, bool) (bool, error)) {
	group.inspectMu.Lock()
	defer group.inspectMu.Unlock()
	if probe == nil {
		group.inspect = descendantsEmpty
		return
	}
	group.inspect = probe
}

func (group *Group) descendantProbe() func(context.Context, int, bool, bool) (bool, error) {
	group.inspectMu.Lock()
	defer group.inspectMu.Unlock()
	return group.inspect
}

func (group *Group) Kill() error {
	group.finishMu.Lock()
	defer group.finishMu.Unlock()
	if group.reaped {
		return nil
	}
	return group.signal(context.Background(), group.leaderHasExited(), signalKill)
}

func (group *Group) signal(ctx context.Context, exited bool, send func(context.Context, int, bool, bool) error) error {
	// SAFETY: Caller cancellation must permit termination.
	inspectionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), processTableTimeout)
	defer cancel()
	return send(inspectionCtx, group.id, exited, group.session)
}

func (group *Group) Finish(ctx context.Context, grace time.Duration) error {
	if err := group.awaitLeader(ctx); err != nil {
		return fmt.Errorf("%w: wait for group %d leader: %w", ErrCleanup, group.id, err)
	}
	return group.finish(ctx, grace)
}

func (group *Group) Terminate(ctx context.Context, grace time.Duration) error {
	group.finishMu.Lock()
	if group.reaped {
		err := group.waitErr
		group.finishMu.Unlock()
		return err
	}
	err := group.signal(ctx, group.leaderHasExited(), signalTerminate)
	group.finishMu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: terminate group %d: %w", ErrCleanup, group.id, err)
	}

	graceCtx, cancel := boundedContext(ctx, grace)
	err = group.awaitLeader(graceCtx)
	cancel()
	if err != nil {
		group.finishMu.Lock()
		if group.reaped {
			waitErr := group.waitErr
			group.finishMu.Unlock()
			return waitErr
		}
		err = group.signal(ctx, group.leaderHasExited(), signalKill)
		group.finishMu.Unlock()
		if err != nil {
			return fmt.Errorf("%w: kill group %d: %w", ErrCleanup, group.id, err)
		}
	}
	if err := group.awaitLeader(ctx); err != nil {
		return fmt.Errorf("%w: wait for killed group %d leader: %w", ErrCleanup, group.id, err)
	}
	return group.finish(ctx, grace)
}

func (group *Group) finish(ctx context.Context, grace time.Duration) error {
	// SAFETY: The cleanup slot gives one caller ownership of reaping. finishMu
	// remains free while descendants drain so Kill can proceed.
	if err := group.acquireCleanup(ctx); err != nil {
		if reaped, reapError := group.reapedResult(); reaped {
			return reapError
		}
		return fmt.Errorf("%w: await group %d cleanup: %w", ErrCleanup, group.id, err)
	}
	defer func() { <-group.cleanup }()
	if reaped, err := group.reapedResult(); reaped {
		return err
	}
	if err := group.leaderExitError(); err != nil {
		return fmt.Errorf("%w: observe group %d leader: %w", ErrCleanup, group.id, err)
	}
	if err := group.emptyDescendants(ctx, grace); err != nil {
		return fmt.Errorf("%w: %w", ErrCleanup, err)
	}
	group.finishMu.Lock()
	defer group.finishMu.Unlock()
	if group.reaped {
		return group.waitErr
	}
	group.waitErr = group.command.Wait()
	group.reaped = true
	group.reapedFlag.Store(true)
	return group.waitErr
}

func (group *Group) acquireCleanup(ctx context.Context) error {
	select {
	case group.cleanup <- struct{}{}:
		return nil
	default:
	}
	select {
	case group.cleanup <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (group *Group) reapedResult() (bool, error) {
	group.finishMu.Lock()
	defer group.finishMu.Unlock()
	return group.reaped, group.waitErr
}

func (group *Group) awaitLeader(ctx context.Context) error {
	select {
	case <-group.leaderDone:
		return group.leaderExitError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (group *Group) leaderExitError() error {
	group.leaderMu.Lock()
	defer group.leaderMu.Unlock()
	return group.leaderErr
}

func (group *Group) leaderHasExited() bool {
	select {
	case <-group.leaderDone:
		return group.leaderExitError() == nil
	default:
		return false
	}
}

func boundedContext(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if duration <= 0 {
		duration = 100 * time.Millisecond
	}
	return context.WithTimeout(parent, duration)
}

func (group *Group) emptyDescendants(ctx context.Context, grace time.Duration) error {
	empty, err := group.descendantProbe()(ctx, group.id, true, group.session)
	if err != nil || empty {
		return err
	}
	if err := group.signal(ctx, true, signalTerminate); err != nil {
		return err
	}
	graceCtx, cancel := boundedContext(ctx, grace)
	err = group.waitDescendantsEmpty(graceCtx)
	cancel()
	if err == nil {
		return nil
	}
	if err := group.signal(ctx, true, signalKill); err != nil {
		return err
	}
	return group.waitDescendantsEmpty(ctx)
}

func (group *Group) waitDescendantsEmpty(ctx context.Context) error {
	poll := newBackoffPoll(guardedSessionPollInterval, descendantMaxPollInterval)
	timer := time.NewTimer(poll.next())
	defer timer.Stop()
	reported := false
	for {
		empty, err := group.descendantProbe()(ctx, group.id, true, group.session)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("group %d descendants remained: %w", group.id, ctx.Err())
		case <-timer.C:
			next := poll.next()
			if !reported && next >= descendantEscalationInterval {
				reported = true
				slog.Warn("process group descendants still live; slowing the wait",
					"group", group.id, "poll_interval", next.String())
			}
			timer.Reset(next)
		}
	}
}

func List() ([]Info, error) { return processTable(context.Background(), false) }

func ListSessions() ([]Info, error) { return processTable(context.Background(), true) }

func IgnoreTermination() { ignoreTermination() }
