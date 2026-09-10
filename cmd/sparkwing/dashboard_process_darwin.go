//go:build darwin

package main

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

func dashboardBoot() (string, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", boot.Sec, boot.Usec), nil
}

func dashboardRunningArtifact() dashboardArtifact {
	return dashboardArtifact{Version: installedVersion(), Revision: readInvokingUpdateIdentity().Revision}
}

func stopOwnedDashboard(record dashboardRecord) error {
	for _, stage := range []struct {
		signal unix.Signal
		wait   time.Duration
	}{{unix.SIGTERM, 5 * time.Second}, {unix.SIGKILL, 2 * time.Second}} {
		owned, err := dashboardOwned(record)
		if err != nil {
			return err
		}
		if !owned {
			return nil
		}
		// Darwin has no pidfd; repeat the birth guard immediately before each signal.
		if err = unix.Kill(record.PID, stage.signal); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
		deadline := time.Now().Add(stage.wait)
		for time.Now().Before(deadline) {
			owned, err = dashboardOwned(record)
			if err != nil {
				return err
			}
			if !owned {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	return errors.New("dashboard did not exit after forced stop")
}
