//go:build !windows

package orchestrator

import "syscall"

func closeOnExec(fd int) { syscall.CloseOnExec(fd) }
