//go:build windows

package jobs

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockTemplateVerifyFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}

func availableTemplateVerifyDisk(path string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
