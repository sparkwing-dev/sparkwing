//go:build windows

package wingd_test

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func processFixtureTempRoot() string { return os.TempDir() }

func processFixtureSuffix() string { return ".exe" }

func killProcessPID(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	return windows.TerminateProcess(handle, 1)
}

func processPIDAlive(pid int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return err
	}
	if exitCode != windowsStillActive {
		return fmt.Errorf("process exited with code %d", exitCode)
	}
	return nil
}
