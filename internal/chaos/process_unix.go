//go:build !windows

package chaos

import (
	"errors"
	"fmt"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func processGroupSupport() error { return nil }

func configureOwnedProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func ignoreProcessGroupTermination() { signal.Ignore(syscall.SIGTERM) }

func processTable() ([]processInfo, error) {
	out, err := exec.Command("ps", "-axo", "pgid=,stat=").Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	processes := make([]processInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pgid, pgidErr := strconv.Atoi(fields[0])
		if pgidErr != nil {
			continue
		}
		processes = append(processes, processInfo{pgid: pgid, state: fields[1]})
	}
	return processes, nil
}

func signalProcessGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return fmt.Errorf("refusing to signal unsafe process group %d", pgid)
	}
	err := syscall.Kill(-pgid, signal)
	// safety: Darwin can report EPERM for a group containing only
	// unsignalable zombies; the bounded group wait still proves cleanup.
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		return nil
	}
	return err
}

func processGroupAlive(pgid int) (bool, error) {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return false, fmt.Errorf("refusing to inspect unsafe process group %d", pgid)
	}
	err := syscall.Kill(-pgid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func waitProcessGroup(pgid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, err := processGroupAlive(pgid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	alive, err := processGroupAlive(pgid)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	return fmt.Errorf("process group %d remained alive after %s", pgid, timeout)
}

func terminateProcessGroup(pgid int, grace, timeout time.Duration) error {
	alive, err := processGroupAlive(pgid)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	if err := signalProcessGroup(pgid, syscall.SIGTERM); err != nil {
		return err
	}
	if err := waitProcessGroup(pgid, grace); err == nil {
		return nil
	}
	if err := signalProcessGroup(pgid, syscall.SIGKILL); err != nil {
		return err
	}
	return waitProcessGroup(pgid, timeout)
}

func killProcessGroup(pgid int) error {
	return signalProcessGroup(pgid, syscall.SIGKILL)
}
