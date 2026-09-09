//go:build !windows

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var processGroupSignal = syscall.Kill

var processSessionID = unix.Getsid

func platformSupport() error { return nil }

func configure(command *exec.Cmd, session bool) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: !session, Setsid: session}
	return nil
}

func ignoreTermination() { signal.Ignore(syscall.SIGTERM) }

func psProcessTable(ctx context.Context, withSessions bool) ([]Info, error) {
	ctx, cancel := context.WithTimeout(ctx, processTableTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,pgid=,stat=").Output()
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", errors.Join(err, ctx.Err()))
	}
	processes, parseErr := parsePSProcessTable(output, withSessions)
	if err := errors.Join(parseErr, ctx.Err()); err != nil {
		return nil, err
	}
	return processes, nil
}

func parsePSProcessTable(output []byte, withSessions bool) ([]Info, error) {
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, errors.New("process listing is empty")
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	processes := make([]Info, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("malformed process row %q", line)
		}
		pid, pidErr := strconv.Atoi(fields[0])
		pgid, pgidErr := strconv.Atoi(fields[1])
		if err := errors.Join(pidErr, pgidErr); err != nil {
			return nil, fmt.Errorf("parse process row %q: %w", line, err)
		}
		if pid < 0 || pgid < 0 || !strings.ContainsRune("DRSTtWXxZIUP", rune(fields[2][0])) {
			return nil, fmt.Errorf("invalid process row %q", line)
		}
		sessionID := 0
		if withSessions {
			var err error
			sessionID, err = processSessionID(pid)
			if errors.Is(err, syscall.ESRCH) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect process %d session: %w", pid, err)
			}
		}
		processes = append(processes, Info{PID: pid, Group: pgid, Session: sessionID, State: fields[2]})
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

func sendSignal(ctx context.Context, leader int, exited bool, signal syscall.Signal) error {
	if err := validateAnchor(leader, exited); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return sendGroupSignal(ctx, leader, signal)
}

func signalTerminate(ctx context.Context, leader int, exited, session bool) error {
	if session {
		return signalSession(ctx, leader, syscall.SIGTERM)
	}
	return sendSignal(ctx, leader, exited, syscall.SIGTERM)
}

func signalKill(ctx context.Context, leader int, exited, session bool) error {
	if session {
		return signalSession(ctx, leader, syscall.SIGKILL)
	}
	return sendSignal(ctx, leader, exited, syscall.SIGKILL)
}

func descendantsEmpty(ctx context.Context, leader int, exited, session bool) (bool, error) {
	if session {
		return sessionDescendantsEmpty(ctx, leader)
	}
	if err := validateAnchor(leader, exited); err != nil {
		return false, err
	}
	processes, err := processTable(ctx, false)
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

func signalSession(ctx context.Context, leader int, signal syscall.Signal) error {
	ctx, cancel := context.WithTimeout(ctx, processTableTimeout)
	defer cancel()
	if leader <= 1 || leader == syscall.Getpgrp() {
		return fmt.Errorf("refusing unsafe process session %d", leader)
	}
	processes, err := sessionProcessTable(ctx, true)
	if err != nil {
		return err
	}
	groups := map[int]bool{}
	for _, process := range processes {
		if process.Session == leader && process.Group > 1 && !process.Exiting && !processTerminated(process.State) {
			groups[process.Group] = true
		}
	}
	orderedGroups := make([]int, 0, len(groups))
	for group := range groups {
		orderedGroups = append(orderedGroups, group)
	}
	slices.Sort(orderedGroups)
	var signalErr error
	for _, group := range orderedGroups {
		signalErr = errors.Join(signalErr, sendGroupSignal(ctx, group, signal))
	}
	return signalErr
}

func sessionDescendantsEmpty(ctx context.Context, leader int) (bool, error) {
	processes, err := processTable(ctx, true)
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

func sendGroupSignal(ctx context.Context, group int, signal syscall.Signal) error {
	err := processGroupSignal(-group, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	// SAFETY: Darwin denies signals during exit; verify every member has committed to exit.
	processes, inspectionErr := sessionProcessTable(ctx, false)
	if inspectionErr != nil {
		return errors.Join(err, inspectionErr)
	}
	for _, process := range processes {
		if process.Group == group && !process.Exiting && !processTerminated(process.State) {
			return err
		}
	}
	return nil
}
