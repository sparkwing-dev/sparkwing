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
	go func() { watchDone <- watchLocalRunner(watchCtx, lifecycle, instance.ID, loop) }()
	defer func() {
		stopWatch()
		if watchErr := <-watchDone; !errors.Is(watchErr, context.Canceled) {
			err = errors.Join(err, watchErr)
		}
	}()
	roll := time.AfterFunc(localRunnerRollAfter, loop.Drain)
	defer roll.Stop()
	drainCtx, stopDrain := context.WithCancel(context.WithoutCancel(ctx))
	drainResult := make(chan error, 1)
	draining := false
	loop.OnClaimsStopped = func() {
		draining = true
		go func() { drainResult <- lifecycle.Drain(drainCtx, instance.ID) }()
	}
	defer func() {
		stopDrain()
		if draining {
			if drainErr := <-drainResult; !errors.Is(drainErr, context.Canceled) {
				err = errors.Join(err, drainErr)
			}
		}
	}()
	return loop.Run(ctx)
}
