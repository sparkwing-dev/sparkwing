//go:build !windows

package sdkinstall

import (
	"os"
	"syscall"
)

func lockTypeScriptInstall(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}
