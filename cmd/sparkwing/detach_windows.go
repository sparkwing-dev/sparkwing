//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

func newDetachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}
