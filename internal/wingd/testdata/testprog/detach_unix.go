//go:build !windows

package main

import "syscall"

func daemonDetachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
