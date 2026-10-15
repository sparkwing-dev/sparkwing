package runners

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// RunnerInstallation preserves registration while draining and replacing managed runner software.
// Drain stops new claims and renews held claims until completion. Replace preserves credentials,
// configuration and executable paths. RefreshManagedUnits preserves controller services and timers.
// Restart uses the existing platform registration, including native Windows replacement.
type RunnerInstallation interface {
	Drain(context.Context) error
	Replace(context.Context, engine.VerifiedRelease) error
	RefreshManagedUnits(context.Context) error
	Restart(context.Context) error
}

// ApplySoftwareUpdate drains before replacement and stops immediately on an installation failure.
func ApplySoftwareUpdate(ctx context.Context, update engine.SoftwareUpdate, installation RunnerInstallation) error {
	if update.Version == update.CurrentVersion && !update.Required {
		return nil
	}
	if installation == nil || update.Release.Version() != update.Version || update.Release.Target().Binary != releaseasset.SparkwingRunner || update.Release.Asset().Name() == "" {
		return fmt.Errorf("runner update has no verified replacement; retry automatic update before restarting")
	}
	for _, step := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"drain held claims", installation.Drain},
		{"replace executable", func(ctx context.Context) error { return installation.Replace(ctx, update.Release) }},
		{"refresh existing services and schedules", installation.RefreshManagedUnits},
		{"restart registered runner", installation.Restart},
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("runner update could not %s: %w; preserve the existing configuration and registration, then retry the update", step.name, err)
		}
	}
	return nil
}
