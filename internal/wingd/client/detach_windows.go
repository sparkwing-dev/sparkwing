//go:build windows

package client

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachSysProcAttr detaches the spawned daemon from the parent's console
// so closing the parent terminal does not close the daemon.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
