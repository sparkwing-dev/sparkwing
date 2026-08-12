//go:build windows

package supervise

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// signalTerminate is a hard kill on Windows -- there is no portable way
// to signal graceful shutdown to a child without an IPC channel, so
// terminate == kill here and the daemon gets no chance to flush.
func signalTerminate(pid int) error {
	return signalKill(pid)
}

// signalKill force-stops pid via TerminateProcess. Exit code 1 is the
// conventional "killed" sentinel.
func signalKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
