//go:build windows

package diskspace

import "golang.org/x/sys/windows"

// Usage reports the bytes available to this user and the volume's total
// size. ok is false where the path cannot be queried, which a caller
// treats as "unknown" rather than zero.
func Usage(path string) (free, total uint64, ok bool) {
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
