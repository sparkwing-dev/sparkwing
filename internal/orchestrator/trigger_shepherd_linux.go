//go:build linux

package orchestrator

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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
	// safety: the host leads a session of its own, so nothing it starts shares this process's group or
	// session, and dies with this process, which the claimer kills when a cancel outlasts its grace. Death
	// signals follow the starting thread, so the thread stays locked until the host is waited for.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	// safety: the claimer cancels by signaling this process's session, which the host is no longer in, so
	// the signal is passed on; caught, not ignored, because an ignored signal stays ignored across exec.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(caught)
	if err := cmd.Start(); err != nil {
		return true, err
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-caught:
				if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
					slog.Default().Warn("pass a cancel on to the trigger host", "err", err)
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	reapDescendants()
	return true, err
}

// safety: bounded, because a descendant that ignores SIGKILL in uninterruptible sleep cannot be waited out.
const orphanReapBudget = 10 * time.Second

// safety: each descendant is signaled through its own group, or alone when that group or its session is
// this process's, so the shepherd never signals itself while anything is left to reap.
func reapDescendants() {
	self := os.Getpid()
	ownGroup, ownSession := unix.Getpgrp(), self
	if sid, err := unix.Getsid(0); err == nil {
		ownSession = sid
	}
	deadline := time.Now().Add(orphanReapBudget)
	for {
		for {
			pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
		}
		left := descendantsOf(self)
		if len(left) == 0 || time.Now().After(deadline) {
			return
		}
		for _, p := range left {
			if p.group != ownGroup && p.group != self && p.session != ownSession {
				kill(-p.group)
			}
			kill(p.pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type procEntry struct{ pid, ppid, group, session int }

func descendantsOf(root int) []procEntry {
	table := procTable()
	byParent := map[int][]procEntry{}
	for _, p := range table {
		byParent[p.ppid] = append(byParent[p.ppid], p)
	}
	var out []procEntry
	for queue := []int{root}; len(queue) > 0; queue = queue[1:] {
		for _, child := range byParent[queue[0]] {
			out = append(out, child)
			queue = append(queue, child.pid)
		}
	}
	return out
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
