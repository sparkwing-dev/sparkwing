//go:build unix

package nodehost

import (
	"errors"
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
	pid      int
	identity procgroup.SessionIdentity
	captured bool
	release  func()
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
		slog.Default().Debug("SDK command session not captured", "err", err)
		return c
	}
	c.identity, c.captured = identity, true
	c.release = recordCommandSession(identity, env)
	return c
}

func (c *commandSession) kill() {
	if c.captured {
		if err := procgroup.KillSession(c.identity); err != nil {
			slog.Default().Debug("kill SDK command session", "err", err)
		}
		return
	}
	if err := syscall.Kill(-c.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Default().Debug("kill SDK command group", "err", err)
	}
}

// perf: one process table read finds live sessions, so thousands of commands cost a kill only where one remains.
// safety: a record whose kill fails stays in the ledger for the crash sweep.
func endCommandSessions(sessions []*commandSession) error {
	if len(sessions) == 0 {
		return nil
	}
	processes, err := procgroup.ListSessions()
	if err != nil {
		return err
	}
	live := map[int]bool{}
	for _, process := range processes {
		if !process.Terminated() {
			live[process.Session] = true
		}
	}
	var errs []error
	for _, c := range sessions {
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
