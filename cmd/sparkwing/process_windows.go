//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess returns for a process
// that hasn't exited yet (STILL_ACTIVE in the Windows SDK).
const stillActive = 259

// processAlive reports whether pid refers to a running process. Uses
// OpenProcess + GetExitCodeProcess; a still-running process reports
// exit code STILL_ACTIVE.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// signalTerminate is a hard kill on Windows -- there is no portable way
// to signal graceful shutdown to a child without an IPC channel.
// will replace this with an HTTP /shutdown endpoint to the dashboard so
// it can drain in-flight requests before exiting. For now, terminate ==
// kill on Windows; callers should not assume the child got a chance to
// flush state.
func signalTerminate(pid int) error {
	return signalKill(pid)
}

// signalKill force-stops pid via TerminateProcess. Exit code 1 is the
// conventional "killed" sentinel; callers reading dashboard.log will see
// the abrupt termination.
func signalKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
