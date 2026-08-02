//go:build windows

package procgroup

import (
	"errors"
	"os/exec"
)

var errUnsupported = errors.New("exact process-group ownership is unavailable on Windows")

func platformSupport() error { return errUnsupported }

func configure(*exec.Cmd, bool) error { return errUnsupported }

func ignoreTermination() {}

func processTable(bool) ([]Info, error) { return nil, errUnsupported }

func waitLeaderExit(int) error { return errUnsupported }

func signalTerminate(int, bool, bool) error { return errUnsupported }

func signalKill(int, bool, bool) error { return errUnsupported }

func descendantsEmpty(int, bool, bool) (bool, error) { return false, errUnsupported }
