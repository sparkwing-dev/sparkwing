//go:build windows

package procgroup

import (
	"context"
	"errors"
	"os/exec"
)

var errUnsupported = errors.New("exact process-group ownership is unavailable on Windows")

func platformSupport() error { return errUnsupported }

func configure(*exec.Cmd, bool) error { return errUnsupported }

func ignoreTermination() {}

func processTable(context.Context, bool) ([]Info, error) { return nil, errUnsupported }

func waitLeaderExit(int) error { return errUnsupported }

func signalTerminate(context.Context, int, bool, bool) error { return errUnsupported }

func signalKill(context.Context, int, bool, bool) error { return errUnsupported }

func descendantsEmpty(context.Context, int, bool, bool) (bool, error) { return false, errUnsupported }
