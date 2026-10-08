//go:build windows

package orchestrator

import (
	"context"
	"strings"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func prepareLocalDispatchAdmission(ctx context.Context, env []string) (func(), error) {
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
	// bug: a daemon first spawned by the dispatched child inherits its kill-on-close Job.
	admission := LocalAdmission{Home: home, Version: sparkwingModuleVersion(), PipelineClient: true}
	cl, err := wingdclient.EnsureDaemon(ctx, admission.clientOptions())
	if err != nil {
		// safety: the child meets the same daemon gap and runs standalone, as a Unix child does.
		if standaloneReasonFor(err) != "" {
			return func() {}, nil
		}
		return nil, err
	}
	return func() { _ = cl.Close() }, nil
}
