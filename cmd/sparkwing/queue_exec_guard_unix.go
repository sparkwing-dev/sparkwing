//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func execQueueExecCommand(command []string) error {
	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	if err := syscall.Exec(path, command, os.Environ()); err != nil {
		return fmt.Errorf("exec command: %w", err)
	}
	return nil
}
