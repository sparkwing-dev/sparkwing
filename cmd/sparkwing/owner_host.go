package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
)

func runOwnerSession(ctx context.Context, award controllerwire.ClaimedUnit, input nodehost.RunInput,
	bind func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error),
) (completion nodehost.RunCompletion, resultErr error) {
	ctx, cancel := award.HostContext(ctx)
	defer cancel()
	if err := context.Cause(ctx); err != nil {
		return completion, err
	}
	session, err := bind(ctx, award)
	if session.close != nil {
		defer func() { resultErr = errors.Join(resultErr, session.close()) }()
	}
	if err != nil {
		return completion, err
	}
	if session.host == nil || session.close == nil {
		return completion, &apierr.Error{Code: apierr.Unavailable, Message: "run hosting requires a host and cleanup binding"}
	}
	input.Storage, input.Engines, input.PlanCache = session.input.Storage, session.input.Engines, session.input.PlanCache
	input.Owner.Authority, input.Owner.CapabilityPrefix = session.input.Owner.Authority, session.input.Owner.CapabilityPrefix
	if err := context.Cause(ctx); err != nil {
		return completion, err
	}
	completion, err = session.host.Run(ctx, input)
	if err := errors.Join(err, context.Cause(ctx)); err != nil {
		return completion, err
	}
	if !isTerminalRunStatus(completion.Outcome) {
		return completion, localRunProtocolError("run host returned no terminal outcome")
	}
	return completion, nil
}
