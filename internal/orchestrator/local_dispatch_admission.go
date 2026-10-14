package orchestrator

import (
	"context"
	"log/slog"
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// safety: the claiming process starts the admission daemon before it starts the trigger's host, because the
// host never starts one: a daemon the host started would be its descendant, which the shepherd that reaps
// the host's leftovers on Linux, and the kill-on-close Job that holds it on Windows, would take down with it.
func prepareLocalDispatchAdmission(ctx context.Context, env []string, logger *slog.Logger) (func(), error) {
	if _, _, ok := wingdclient.ResolveHostBin(); !ok {
		return func() {}, nil
	}
	var home string
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "SPARKWING_HOME") {
			home = value
		}
	}
	admission := LocalAdmission{Home: home, Version: sparkwingModuleVersion(), PipelineClient: true}
	cl, err := wingdclient.EnsureDaemon(ctx, admission.clientOptions())
	if err != nil {
		// safety: the host meets the same daemon gap and runs standalone, saying so.
		if standaloneReasonFor(err) != "" {
			logger.Warn("local trigger: the admission daemon is not running and could not be started; the run is admitted without it", "err", err)
			return func() {}, nil
		}
		return nil, err
	}
	return func() {
		if err := cl.Close(); err != nil {
			logger.Debug("local trigger: close the admission daemon probe", "err", err)
		}
	}, nil
}
