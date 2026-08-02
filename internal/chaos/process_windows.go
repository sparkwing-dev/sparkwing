//go:build windows

package chaos

import (
	"errors"
	"os/exec"
	"time"
)

var errProcessGroupsUnsupported = errors.New("exact process-group ownership is unavailable on Windows")

func processGroupSupport() error { return errProcessGroupsUnsupported }

func configureOwnedProcessGroup(*exec.Cmd) error { return errProcessGroupsUnsupported }

func ignoreProcessGroupTermination() {}

func processTable() ([]processInfo, error) { return nil, errProcessGroupsUnsupported }

func processGroupAlive(int) (bool, error) { return false, errProcessGroupsUnsupported }

func waitProcessGroup(int, time.Duration) error { return errProcessGroupsUnsupported }

func terminateProcessGroup(int, time.Duration, time.Duration) error {
	return errProcessGroupsUnsupported
}

func killProcessGroup(int) error { return errProcessGroupsUnsupported }
