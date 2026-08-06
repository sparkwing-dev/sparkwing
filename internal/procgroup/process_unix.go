//go:build !windows

package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func platformSupport() error { return nil }

func configure(cmd *exec.Cmd, session bool) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: !session, Setsid: session}
	return nil
}

func ignoreTermination() { signal.Ignore(syscall.SIGTERM) }

func processTable(withSessions bool) ([]Info, error) {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=,stat=").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	processes := make([]Info, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, pgidErr := strconv.Atoi(fields[1])
		if pidErr != nil || pgidErr != nil {
			continue
		}
		sid := 0
		if withSessions {
			sid, _ = unix.Getsid(pid)
		}
		processes = append(processes, Info{PID: pid, Group: pgid, Session: sid, State: fields[2]})
	}
	return processes, nil
}

func validateAnchor(leader int, exited bool) error {
	if leader <= 1 || leader == syscall.Getpgrp() {
		return fmt.Errorf("refusing unsafe process group %d", leader)
	}
	if exited {
		return nil
	}
	pgid, err := syscall.Getpgid(leader)
	if err != nil {
		return fmt.Errorf("ownership anchor %d unavailable: %w", leader, err)
	}
	if pgid != leader {
		return fmt.Errorf("ownership anchor %d moved to process group %d", leader, pgid)
	}
	return nil
}

func sendSignal(leader int, exited bool, sig syscall.Signal) error {
	if err := validateAnchor(leader, exited); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	err := syscall.Kill(-leader, sig)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

func signalTerminate(leader int, exited, session bool) error {
	if session {
		return signalSession(leader, syscall.SIGTERM)
	}
	return sendSignal(leader, exited, syscall.SIGTERM)
}

func signalKill(leader int, exited, session bool) error {
	if session {
		return signalSession(leader, syscall.SIGKILL)
	}
	return sendSignal(leader, exited, syscall.SIGKILL)
}

func descendantsEmpty(leader int, exited, session bool) (bool, error) {
	if session {
		return sessionDescendantsEmpty(leader)
	}
	if err := validateAnchor(leader, exited); err != nil {
		return false, err
	}
	processes, err := processTable(false)
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		if process.Group == leader && process.PID != leader {
			return false, nil
		}
	}
	return true, nil
}

func signalSession(leader int, sig syscall.Signal) error {
	if leader <= 1 || leader == syscall.Getpgrp() {
		return fmt.Errorf("refusing unsafe process session %d", leader)
	}
	processes, err := processTable(true)
	if err != nil {
		return err
	}
	groups := map[int]bool{}
	for _, process := range processes {
		if process.Session == leader && process.Group > 1 {
			groups[process.Group] = true
		}
	}
	for group := range groups {
		err := syscall.Kill(-group, sig)
		if err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
			return err
		}
	}
	return nil
}

func sessionDescendantsEmpty(leader int) (bool, error) {
	processes, err := processTable(true)
	if err != nil {
		return false, err
	}
	for _, process := range processes {
		if process.Session == leader && process.PID != leader {
			return false, nil
		}
	}
	return true, nil
}
