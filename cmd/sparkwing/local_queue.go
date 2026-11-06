package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func runLocalQueue(ctx context.Context, options client.EngineOptions, loop *runners.Loop) (err error) {
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
	options.Bearer = func(ctx context.Context) (string, error) { return credential.Token, context.Cause(ctx) }
	loop.Controller, err = client.NewEngineClient(options)
	if err != nil {
		return err
	}
	if loop.OnError == nil {
		loop.OnError = func(err error) { slog.Error("local run host failed", "err", err) }
	}
	return loop.Run(ctx)
}
