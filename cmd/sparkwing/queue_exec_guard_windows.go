//go:build windows

package main

import "errors"

func execQueueExecCommand([]string) error {
	return errors.New("queue exec guard: process-session ownership is unavailable on Windows")
}
