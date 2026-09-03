//go:build !windows

package wingd_test

import "syscall"

func processFixtureTempRoot() string { return "/tmp" }

func processFixtureSuffix() string { return "" }

func killProcessPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

func processPIDAlive(pid int) error { return syscall.Kill(pid, 0) }
