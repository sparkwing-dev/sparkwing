package main

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const fixtureUserManagerMarker = "sparkwing-fixture-unmanaged-runner-only"

func fixtureUseUnmanagedUserManager() {
	startLocalRunnerProcess = func(ctx context.Context, host crontimer.Host, id string) error {
		if err := wingd.StartLocalRunnerProcess(ctx, fixtureUnmanagedRunnerHost(host), id); err != nil {
			return fmt.Errorf("%s: %w", fixtureUserManagerMarker, err)
		}
		return nil
	}
	waitLocalRunnerShutdown = func(ctx context.Context, host crontimer.Host) error {
		return wingd.WaitLocalRunnerShutdown(ctx, fixtureUnmanagedRunnerHost(host))
	}
}

func fixtureUnmanagedRunnerHost(host crontimer.Host) crontimer.Host {
	// safety: explicit platform probes belong to native-manager unit tests; preserve their injected executor.
	if host.Stat == nil {
		host.GOOS = "windows"
	}
	return host
}
