//go:build !windows

package main

import "syscall"

func newDetachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
