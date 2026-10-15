package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: plan code and every node run in their own processes with the minimal environment, so the profile's
// token stays here. The claim is renewed from before the build, so a cold build outlasting its lease keeps it; a
// host shutting down leaves the trigger to that lease, and a run that never started fails here, or is cancelled.
func hostTrigger(ctx context.Context, cli *client.Client, trigger *store.Trigger, controllerURL, token string, heartbeat time.Duration) error {
	return hostTriggerWith(ctx, cli, trigger, controllerURL, token, heartbeat, buildCheckoutPipeline, orchestrator.HostTriggerRun)
}

type checkoutBuild func(ctx context.Context) (entry pipelineentry.Command, dir string, release func(), err error)

func hostTriggerWith(ctx context.Context, cli *client.Client, trigger *store.Trigger, controllerURL, token string,
	heartbeat time.Duration, build checkoutBuild, host func(context.Context, orchestrator.TriggerHost) error,
) error {
	keeper := lease.Keeper{Interval: heartbeat, Lease: store.DefaultLeaseDuration, Renew: func(ctx context.Context) (lease.Beat, error) {
		status, err := cli.HeartbeatTrigger(ctx, trigger.ID)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld) || client.IsTokenDead(err) {
			return lease.Beat{}, &apierr.Error{Code: apierr.LockHeld, Message: "trigger claim ended"}
		}
		if err != nil {
			return lease.Beat{}, err
		}
		return lease.Beat{CancelRequested: status != nil && status.CancelRequested}, nil
	}}
	runCtx, stop := keeper.Run(orchestrator.DispatchContext(ctx, trigger))
	defer stop()
	err := hostTriggerFromCheckout(runCtx, trigger, controllerURL, token, build, host)
	cause := context.Cause(runCtx)
	if err == nil || errors.Is(cause, context.Canceled) {
		return err
	}
	status := "failed"
	if cancel := (*lease.CancelError)(nil); errors.As(cause, &cancel) {
		status = "cancelled"
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
	defer cancel()
	if ferr := cli.FinishRun(finishCtx, trigger.ID, status, err.Error()); ferr != nil &&
		!orchestrator.ConfirmTerminalRunAfterWriteError(finishCtx, cli, trigger.ID) {
		return errors.Join(err, fmt.Errorf("finish the run: %w", ferr))
	}
	if ferr := cli.FinishTrigger(finishCtx, trigger.ID); ferr != nil {
		return errors.Join(err, fmt.Errorf("finish the trigger: %w", ferr))
	}
	return err
}

func buildCheckoutPipeline(ctx context.Context) (entry pipelineentry.Command, dir string, release func(), err error) {
	sparkwingDir, err := findSparkwingDir()
	if err != nil {
		return nil, "", nil, err
	}
	ts, err := pipelineentry.IsTypeScript(sparkwingDir)
	if err != nil {
		return nil, "", nil, err
	}
	if ts {
		entry, err := orchestrator.PrepareTypeScriptOnRunner(ctx, sparkwingDir)
		return entry, filepath.Dir(sparkwingDir), func() {}, err
	}
	key, keyParts, err := bincache.ExplainCacheKey(sparkwingDir, os.Environ())
	if err != nil {
		return nil, "", nil, fmt.Errorf("read .sparkwing/: %w", err)
	}
	lease, _, err := pipelineBinary(ctx, sparkwingDir, key, keyParts, os.Environ(), os.Stderr)
	if err != nil {
		return nil, "", nil, fmt.Errorf("build the pipeline: %w", err)
	}
	return pipelineentry.Binary(lease.Path()), filepath.Dir(sparkwingDir), func() {
		if err := lease.Release(); err != nil {
			slog.Default().Debug("pipeline binary lease release failed", "err", err)
		}
	}, nil
}

func hostTriggerFromCheckout(ctx context.Context, trigger *store.Trigger, controllerURL, token string,
	build checkoutBuild, host func(context.Context, orchestrator.TriggerHost) error,
) error {
	entry, dir, release, err := build(ctx)
	if err != nil {
		return err
	}
	defer release()
	return host(ctx, orchestrator.TriggerHost{
		ControllerURL: controllerURL, Token: token, Trigger: trigger, Entry: entry, Dir: dir,
	})
}
