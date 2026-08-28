//go:build windows

package main

import (
	"fmt"

	"golang.org/x/sys/windows"
)

const stillActive = 259

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
