//go:build !windows

package client

import "syscall"

// detachSysProcAttr detaches the spawned daemon from the parent's
// controlling terminal and process group so it survives the parent
// exiting and a Ctrl-C in the parent's shell.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
