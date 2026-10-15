//go:build windows

package runners

import (
	"os"

	"golang.org/x/sys/windows"
)

func replaceInstalledRunner(source, target string) error {
	return replaceRunningRunner(source, target, moveRunnerFile, os.Remove)
}

func moveRunnerFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
