package runners

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

// RunnerInstallation keeps software replacement behind the platform's existing service manager.
// Drain stops new claims while renewing held claims until completion. Replace accepts only a
// verified runner asset and preserves credentials, configuration, executable path and registration.
// RefreshManagedUnits re-renders existing service/timer definitions without deleting schedules.
// Restart uses the preserved registration, including the native Windows replacement path.
type RunnerInstallation interface {
	Drain(context.Context) error
	Replace(context.Context, engine.VerifiedRelease) error
	RefreshManagedUnits(context.Context) error
	Restart(context.Context) error
}

// ApplySoftwareUpdate installs a prepared verified runner after draining, refreshes its existing
// units and restarts it. Failure is actionable and leaves the prior registration/config recoverable.
// The controller checks the same signed minimum before awarding a new claim to the resumed runner.
func ApplySoftwareUpdate(ctx context.Context, update engine.SoftwareUpdate, installation RunnerInstallation) error {
	panic("unimplemented: c3-engine")
}
