//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func dashboardBoot() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

func dashboardRunningArtifact() dashboardArtifact {
	// /proc/self/exe retains the executing inode after the install path changes.
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return dashboardArtifact{}
	}
	defer f.Close()
	return dashboardOpenArtifact(f)
}

func stopOwnedDashboard(r dashboardRecord) error {
	fd, err := unix.PidfdOpen(r.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dashboard process handle: %w", err)
	}
	defer func() { dashboardCleanupError("close dashboard process handle", unix.Close(fd)) }()
	owned, err := dashboardOwned(r)
	if err != nil {
		return err
	}
	if !owned {
		return nil
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	if waitDashboardHandle(fd, 5*time.Second) {
		return nil
	}
	if err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	if !waitDashboardHandle(fd, 2*time.Second) {
		return errors.New("dashboard process did not exit after forced stop")
	}
	return nil
}

func waitDashboardHandle(fd int, timeout time.Duration) bool {
	polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(polls, int(timeout.Milliseconds()))
	return err == nil && polls[0].Revents&unix.POLLIN != 0
}
