package orchestrator

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/internal/runners/warmpool"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BuildWarmRunnerFactory gives each trigger a fresh warm-pool runner with its own fallback handoff.
func BuildWarmRunnerFactory(
	controllerURL, token string,
	cfg warmpool.Config,
	fallbackFactory func(Backends, *store.Trigger) runner.Runner,
) func(Backends, *store.Trigger) runner.Runner {
	return func(backends Backends, trigger *store.Trigger) runner.Runner {
		ctrl := client.NewWithToken(controllerURL, &http.Client{Timeout: 30 * time.Second}, token)
		var fallback runner.Runner
		if fallbackFactory != nil {
			fallback = fallbackFactory(backends, trigger)
		}
		return warmpool.New(ctrl, fallback, cfg, slog.Default())
	}
}
