//go:build unix

package nodehost

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

// safety: cancellation kills the whole session, including children in other process groups; a normal
// exit leaves background children running, as SDK-run commands did, until the SDK session ends.
type commandSession struct {
	pid        int
	identity   procgroup.SessionIdentity
	captured   bool
	captureErr error
	cleanupErr error
	release    func()
}

func execFormatError(err error) bool { return errors.Is(err, syscall.ENOEXEC) }

func bufferedBytes(f *os.File) int {
	conn, err := f.SyscallConn()
	if err != nil {
		return 0
	}
	if fionread == 0 {
		return 0
	}
	n, ioctlErr := 0, error(nil)
	if err := conn.Control(func(fd uintptr) { n, ioctlErr = unix.IoctlGetInt(int(fd), fionread) }); err != nil || ioctlErr != nil {
		return 0
	}
	return n
}

func configureHostCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// safety: capture before the leader can be reaped; the ledger keeps it for crash cleanup until the session ends.
func trackHostCommand(cmd *exec.Cmd, env []string) *commandSession {
	c := &commandSession{pid: cmd.Process.Pid, release: func() {}}
	identity, err := procgroup.CaptureSession(c.pid)
	if err != nil {
		c.captureErr = fmt.Errorf("capture SDK command session %d: %w", c.pid, err)
		slog.Default().Debug("SDK command session not captured", "err", c.captureErr)
		return c
	}
	c.identity, c.captured = identity, true
	c.release = recordCommandSession(identity, env)
	return c
}

func (c *commandSession) leader() (int, string, bool) {
	return c.identity.LeaderPID, c.identity.BirthToken, c.captured
}

func (c *commandSession) kill() error {
	if c.captured {
		if err := procgroup.KillSession(c.identity); err != nil {
			return fmt.Errorf("kill SDK command session %d: %w", c.pid, err)
		}
		return nil
	}
	err := syscall.Kill(-c.pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		err = nil
	}
	if err != nil {
		err = fmt.Errorf("kill SDK command group %d: %w", c.pid, err)
	}
	return errors.Join(c.captureErr, err)
}

// safety: a later successful cleanup cannot erase the failure of the running attempt.
func (c *commandSession) failure() error {
	if c.captureErr == nil || errors.Is(c.cleanupErr, c.captureErr) {
		return c.cleanupErr
	}
	return errors.Join(c.cleanupErr, c.captureErr)
}

// perf: one process table read finds live sessions, so thousands of commands cost a kill only where one remains.
// safety: a record whose kill fails stays in the ledger for the crash sweep.
func endCommandSessions(sessions []*commandSession) error {
	if len(sessions) == 0 {
		return nil
	}
	var errs []error
	for _, session := range sessions {
		errs = append(errs, session.failure())
	}
	processes, err := procgroup.ListSessions()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	live := map[int]bool{}
	for _, process := range processes {
		if !process.Terminated() {
			live[process.Session] = true
		}
	}
	for _, c := range sessions {
		if !c.captured && c.captureErr != nil {
			continue
		}
		if c.captured && live[c.identity.SessionID] {
			if err := procgroup.KillSession(c.identity); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		c.release()
	}
	return errors.Join(errs...)
}

// safety: matches the record the SDK writes for its own commands, so a sweep ends the session if this host dies.
func recordCommandSession(leader procgroup.SessionIdentity, env []string) func() {
	var run, node string
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "SPARKWING_RUN_ID="); ok {
			run = value
		}
		if value, ok := strings.CutPrefix(entry, "SPARKWING_JOB_ID="); ok {
			node = value
		}
	}
	if run == "" || node == "" {
		return func() {}
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return func() {}
	}
	ownerBirth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		slog.Default().Debug("SDK command session not recorded", "err", err)
		return func() {}
	}
	release, err := sessionledger.Open(p.SessionLedgerDir()).Record(sessionledger.Record{
		Run: run, Node: node, OwnerPID: os.Getpid(), OwnerBirth: ownerBirth,
		Handle: sessionledger.Handle{Kind: "session", LeaderPID: leader.LeaderPID, SessionID: leader.SessionID, LeaderBirth: leader.BirthToken},
	})
	if err != nil {
		slog.Default().Debug("SDK command session not recorded", "err", err)
		return func() {}
	}
	return release
}

func commandPipe() (*os.File, *os.File, error) { return os.Pipe() }

func startHostCommand(cmd *exec.Cmd, env []string) (*commandSession, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return trackHostCommand(cmd, env), nil
}

func (*commandSession) signaled(cmd *exec.Cmd) bool { return !cmd.ProcessState.Exited() }
