//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// newDetachSysProcAttr returns a SysProcAttr that detaches the child from
// the parent's console. CREATE_NEW_PROCESS_GROUP lets us send Ctrl-Break
// to the group later if we want graceful shutdown; DETACHED_PROCESS hides
// the child from the parent's console so closing the parent terminal
// doesn't close the supervisor.
func newDetachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
