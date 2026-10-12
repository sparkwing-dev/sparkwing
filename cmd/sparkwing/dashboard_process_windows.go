//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hack: x/sys exposes the information class without its native structure.
type dashboardBootEnvironment struct {
	Identifier windows.GUID
	Firmware   uint32
	Padding    uint32
	Flags      uint64
}

func dashboardBoot() (string, error) {
	var boot dashboardBootEnvironment
	var size uint32
	if err := windows.NtQuerySystemInformation(windows.SystemBootEnvironmentInformation, unsafe.Pointer(&boot), uint32(unsafe.Sizeof(boot)), &size); err != nil {
		return "", fmt.Errorf("read dashboard boot identity: %w", err)
	}
	if size != uint32(unsafe.Sizeof(boot)) || boot.Identifier == (windows.GUID{}) {
		return "", errors.New("Windows returned no dashboard boot identity")
	}
	return boot.Identifier.String(), nil
}

func dashboardRunningArtifact() dashboardArtifact {
	path, err := os.Executable()
	if err != nil {
		return dashboardArtifact{}
	}
	return dashboardFileArtifact(path)
}

func stopOwnedDashboard(record dashboardRecord) error {
	if record.PID <= 0 || uint64(record.PID) > uint64(^uint32(0)) {
		return errors.New("invalid dashboard PID; no process was changed")
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(record.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dashboard process handle: %w", err)
	}
	defer func() { dashboardCleanupError("close dashboard process handle", windows.CloseHandle(handle)) }()
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return err
	}
	if state == windows.WAIT_OBJECT_0 {
		return nil
	}
	boot, err := dashboardBoot()
	if err != nil {
		return err
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return err
	}
	if record.Boot != boot || record.Birth != strconv.FormatInt(creation.Nanoseconds(), 10) {
		return errors.New("dashboard PID belongs to another process incarnation")
	}
	// safety: identity and termination use the same handle, so PID reuse cannot select another process.
	if err := windows.TerminateProcess(handle, 0); err != nil {
		state, waitErr := windows.WaitForSingleObject(handle, 0)
		if waitErr == nil && state == windows.WAIT_OBJECT_0 {
			return nil
		}
		return fmt.Errorf("stop dashboard process: %w", err)
	}
	state, err = windows.WaitForSingleObject(handle, 2000)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return errors.New("dashboard process did not exit after forced stop")
	}
	return nil
}
