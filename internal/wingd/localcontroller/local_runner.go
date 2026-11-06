package localcontroller

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func runnerInstance(instance store.LocalRunnerInstance) localhostwire.LocalRunnerInstance {
	return localhostwire.LocalRunnerInstance{
		ID: instance.ID, CapabilityPrefix: instance.CapabilityPrefix,
		PID: instance.PID, ProcessBirth: instance.ProcessBirth, Version: instance.Version,
		BuildIdentity: instance.BuildIdentity, Machine: instance.Machine, State: instance.State,
	}
}

func (c *localController) RegisterLocalRunner(ctx context.Context, subject string, instance localhostwire.LocalRunnerInstance) (localhostwire.LocalRunnerInstance, error) {
	result, err := c.operator.RegisterLocalRunner(ctx, subject, store.LocalRunnerInstance{
		ID: instance.ID, CapabilityPrefix: instance.CapabilityPrefix, PID: instance.PID,
		ProcessBirth: instance.ProcessBirth, Version: instance.Version, BuildIdentity: instance.BuildIdentity,
	})
	return runnerInstance(result), localRunnerError(err)
}

func (c *localController) ReadLocalRunner(ctx context.Context, subject, id string) (localhostwire.LocalRunnerInstance, error) {
	result, err := c.operator.ReadLocalRunner(ctx, subject, id)
	return runnerInstance(result), localRunnerError(err)
}

func (c *localController) ActivateLocalRunner(ctx context.Context, subject, id string) error {
	return localRunnerError(c.operator.ActivateLocalRunner(ctx, subject, id))
}

func (c *localController) DrainLocalRunner(ctx context.Context, subject, id string) error {
	return localRunnerError(c.operator.DrainLocalRunner(ctx, subject, id))
}

func (c *localController) StopLocalRunner(ctx context.Context, subject, id string) error {
	return localRunnerError(c.operator.StopLocalRunner(ctx, subject, id))
}

func localRunnerError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return &apierr.Error{Code: apierr.NotFound, Message: "local runner instance not found"}
	}
	return err
}
