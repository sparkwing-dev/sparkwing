//go:build windows

package main

import "errors"

func runQueueExecCommand([]string) error {
	return errors.New("queue exec guard: process-session ownership is unavailable on Windows")
}
