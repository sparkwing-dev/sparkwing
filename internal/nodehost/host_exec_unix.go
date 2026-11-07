//go:build unix

package nodehost

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

// commandSession is a host command's own Unix session. Cancellation kills it whole,
// including children that moved to another process group. A normal exit leaves its
// background children running, as SDK-run commands did, until the SDK session ends.
type commandSession struct {
	pid      int
	identity procgroup.SessionIdentity
	captured bool
	release  func()
}

func configureHostCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// trackHostCommand captures the session before the leader can be reaped and records it
// for crash cleanup until the SDK session ends.
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

// endCommandSessions kills what the SDK session's commands left running. One process
// table read finds the live sessions, so a session that ran thousands of commands pays
// for a kill only where something remains. A record whose kill fails stays in the ledger.
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

// recordCommandSession writes the ledger record a sweep needs to end the session if
// this host dies first, matching what the SDK records for commands it runs itself.
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
