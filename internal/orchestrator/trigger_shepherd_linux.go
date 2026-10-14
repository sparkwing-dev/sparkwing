//go:build linux

package orchestrator

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// ShepherdTrigger runs the trigger host, argv, as this process's only child
// and, once it exits however it exits, kills every process it left: node
// processes lead sessions of their own, so a killed host's nodes would
// otherwise run on with their capacity released. It reports false where it
// cannot, and the caller hosts the trigger itself.
func ShepherdTrigger(argv []string) (bool, error) {
	// safety: as a child subreaper this process inherits every orphan of the host's tree, and it starts
	// nothing else, so whatever it inherits is the host's to reap; a pipeline cannot add to that set.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return false, nil
	}
	// safety: the claimer signals this whole session to cancel, which reaches the host directly; the host,
	// not this process, decides how its run ends. The signals are caught rather than ignored, because an
	// ignored signal stays ignored across exec and the host would never see the cancel.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(caught)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	reapOrphans()
	return true, err
}

// safety: bounded, because a descendant that ignores SIGKILL in uninterruptible sleep cannot be waited out.
const orphanReapBudget = 10 * time.Second

func reapOrphans() {
	deadline := time.Now().Add(orphanReapBudget)
	daemon := daemonSession()
	for {
		for {
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
		orphans := slices.DeleteFunc(childrenOf(os.Getpid()), func(p procEntry) bool { return daemon > 0 && p.session == daemon })
		if len(orphans) == 0 || time.Now().After(deadline) {
			return
		}
		for _, p := range orphans {
			// safety: a session leader takes its whole session with it, as a node process's cleanup would.
			if p.session == p.pid {
				killSessionMembers(p.session)
			}
			kill(-p.group)
			kill(p.pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// safety: the host may start this home's admission daemon, which is meant to outlive it, so the daemon's
// session, found from whoever owns the admission socket, is spared; it is the only process left running.
func daemonSession() int {
	paths, err := DefaultPaths()
	if err != nil {
		return 0
	}
	sock, err := wingd.SocketPath(paths.Root)
	if err != nil {
		return 0
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return 0
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Default().Debug("close the admission socket probe", "err", err)
		}
	}()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0
	}
	var cred *unix.Ucred
	if cerr := raw.Control(func(fd uintptr) { cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); cerr != nil || err != nil || cred == nil {
		return 0
	}
	sid, err := unix.Getsid(int(cred.Pid))
	if err != nil {
		return 0
	}
	return sid
}

type procEntry struct{ pid, ppid, group, session int }

func childrenOf(parent int) []procEntry {
	var out []procEntry
	for _, p := range procTable() {
		if p.ppid == parent {
			out = append(out, p)
		}
	}
	return out
}

func killSessionMembers(session int) {
	for _, p := range procTable() {
		if p.session == session {
			kill(-p.group)
		}
	}
}

// safety: a process already gone is what the sweep wants; anything else is logged, and the next pass retries.
func kill(target int) {
	if err := unix.Kill(target, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		slog.Default().Warn("kill a process the trigger host left", "target", target, "err", err)
	}
}

func procTable() []procEntry {
	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		return nil
	}
	out := make([]procEntry, 0, len(stats))
	for _, path := range stats {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// safety: the command name may hold spaces and parentheses, so fields are read after its last ')'.
		i := bytes.LastIndexByte(raw, ')')
		if i < 0 {
			continue
		}
		fields := bytes.Fields(raw[i+1:])
		if len(fields) < 5 || bytes.Equal(fields[0], []byte("Z")) {
			continue
		}
		pid, err := strconv.Atoi(filepath.Base(filepath.Dir(path)))
		if err != nil {
			continue
		}
		ppid, err1 := strconv.Atoi(string(fields[1]))
		group, err2 := strconv.Atoi(string(fields[2]))
		session, err3 := strconv.Atoi(string(fields[3]))
		if errors.Join(err1, err2, err3) != nil {
			continue
		}
		out = append(out, procEntry{pid: pid, ppid: ppid, group: group, session: session})
	}
	return out
}
