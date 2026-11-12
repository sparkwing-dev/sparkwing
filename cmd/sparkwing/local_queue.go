package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

// safety: a runner drains well before its claim token expires so a fresh instance and token replace it.
const localRunnerRollAfter = controller.LocalClaimantLifetime / 7

func runLocalQueue(ctx context.Context, id string, options client.EngineOptions, loop *runners.Loop, lifecycle localRunnerLifecycle) (err error) {
	if loop == nil || loop.CheckVersion == nil || lifecycle == nil {
		return errors.New("local queue requires a loop, version policy and process lifecycle")
	}
	tokens, err := client.NewTokenManager(options, false)
	if err != nil {
		return err
	}
	credential, err := tokens.CreateToken(ctx, controllerwire.TokenCreate{Scopes: []capability.Grant{capability.TriggersClaim, capability.NodesClaim}})
	if err != nil {
		return err
	}
	defer func() {
		// safety: hosts finish cleanup over the API before their claim authority is revoked.
		_, revokeErr := tokens.RevokeToken(context.WithoutCancel(ctx), credential.Metadata.Prefix)
		err = errors.Join(err, revokeErr)
	}()
	instance, err := localRunnerInstance(id, credential.Metadata.Prefix, loop.Config.Version)
	if err != nil {
		return err
	}
	if _, err := lifecycle.Register(ctx, instance); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lifecycle.Stop(context.WithoutCancel(ctx), instance.ID)) }()
	if err := loop.CheckVersion(ctx, loop.Config.Version); err != nil {
		return err
	}
	if err := lifecycle.Activate(ctx, instance.ID); err != nil {
		return err
	}
	options.Bearer = func(ctx context.Context) (string, error) { return credential.Token, context.Cause(ctx) }
	loop.Controller, err = client.NewEngineClient(options)
	if err != nil {
		return err
	}
	if loop.OnError == nil {
		loop.OnError = func(err error) { slog.Error("local run host failed", "err", err) }
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	watchDone := make(chan error, 1)
	go func() {
		watchErr := watchLocalRunner(watchCtx, lifecycle, instance.ID, loop)
		watchDone <- localRunnerCleanupError(watchCtx, watchErr)
	}()
	defer func() {
		stopWatch()
		err = errors.Join(err, <-watchDone)
	}()
	roll := time.AfterFunc(localRunnerRollAfter, loop.Drain)
	defer roll.Stop()
	drainCtx, stopDrain := context.WithCancel(context.WithoutCancel(ctx))
	drainResult := make(chan error, 1)
	draining := false
	claimsStopped := loop.OnClaimsStopped
	loop.OnClaimsStopped = func() {
		draining = true
		go func() {
			drainErr := lifecycle.Drain(drainCtx, instance.ID)
			drainResult <- localRunnerCleanupError(drainCtx, drainErr)
		}()
		if claimsStopped != nil {
			claimsStopped()
		}
	}
	defer func() {
		stopDrain()
		if draining {
			err = errors.Join(err, <-drainResult)
		}
	}()
	return loop.Run(ctx)
}

// safety: classify at completion so a later cleanup cancellation cannot hide an earlier failure.
func localRunnerCleanupError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() == nil {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		if len(parts) == 0 {
			return err
		}
		for _, part := range parts {
			if part == nil || localRunnerCleanupError(ctx, part) != nil {
				return err
			}
		}
		return nil
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		if localRunnerCleanupError(ctx, wrapped) == nil {
			return nil
		}
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return err
}
