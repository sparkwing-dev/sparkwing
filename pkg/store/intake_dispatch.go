package store

import (
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SelectDispatch is the one intake dispatch rule on every controller. An executing run that selects no runner or
// pool runs anywhere under controller dispatch; local, burst, publish and explicitly placed runs keep host dispatch.
func SelectDispatch(request runspec.Request) runspec.Dispatch {
	runner := request.Options.Runner
	if request.Mode != runspec.IntakeExecute || request.Origin == runspec.OriginLocal || request.Burst != nil || runner.Set && runner.Value.Kind != runspec.RunnerAny || slices.ContainsFunc(request.Options.Requires.Value, requiresPool) {
		return runspec.DispatchHost
	}
	return runspec.DispatchController
}

func requiresPool(term string) bool {
	for _, alternative := range strings.Split(term, ",") {
		if strings.HasPrefix(strings.TrimSpace(alternative), "pool=") {
			return true
		}
	}
	return false
}
