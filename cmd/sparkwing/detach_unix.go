//go:build !windows

package main

import "syscall"

// newDetachSysProcAttr returns a SysProcAttr that detaches the child
// from the parent's controlling terminal and process group. Used by
// `sparkwing dashboard start` so the supervisor survives the parent
// returning, and so a Ctrl-C in the parent shell doesn't propagate to
// it via the terminal's foreground process group.
func newDetachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
