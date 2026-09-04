//go:build windows

package orchestrator_test

import (
	"os"

	"golang.org/x/sys/windows"
)

const processStillActive = 259

func processAlive(pid int) bool {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(process, &exitCode); err != nil {
		return true
	}
	return exitCode == processStillActive
}

func killProcessForTest(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
