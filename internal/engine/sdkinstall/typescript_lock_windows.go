package sdkinstall

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockTypeScriptInstall(file *os.File) error {
	// safety: npm reads the manifest through another handle, so lock beyond its contents.
	overlapped := windows.Overlapped{OffsetHigh: 1}
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}
