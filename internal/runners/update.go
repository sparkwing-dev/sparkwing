package runners

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// RunnerInstallation preserves registration while draining and replacing managed runner software.
type RunnerInstallation interface {
	Apply(context.Context, engine.VerifiedRelease) error
}

// applySoftwareUpdate drains before replacement and stops immediately on an installation failure.
func applySoftwareUpdate(ctx context.Context, update engine.SoftwareUpdate, installation RunnerInstallation) error {
	if update.Version == update.CurrentVersion && !update.Required {
		return nil
	}
	if installation == nil || update.Release.Version() != update.Version || update.Release.Target().Binary != releaseasset.SparkwingRunner || update.Release.Asset().Name() == "" {
		return fmt.Errorf("runner update has no verified replacement; retry automatic update before restarting")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return installation.Apply(ctx, update.Release)
}
