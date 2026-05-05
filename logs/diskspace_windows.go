//go:build windows

package logs

import "golang.org/x/sys/windows"

func diskSpace(path string) (free, total uint64, ok bool) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, false
	}
	var freeAvailableToCaller, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &freeAvailableToCaller, &totalBytes, &totalFree); err != nil {
		return 0, 0, false
	}
	return freeAvailableToCaller, totalBytes, true
}
