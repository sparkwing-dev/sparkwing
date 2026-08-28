//go:build windows

package supervise

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func signalTerminate(pid int) error {
	return signalKill(pid)
}

func signalKill(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
