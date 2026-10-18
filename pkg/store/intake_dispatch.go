package store

import (
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SelectDispatch is the one intake dispatch rule. Off Cloud the request's own
// dispatch stands; on Cloud a runner selector or pool requirement keeps the run
// host-dispatched and every other executable run goes to the launcher.
func SelectDispatch(cloud bool, request runspec.Request) runspec.Dispatch {
	if !cloud {
		return request.Dispatch
	}
	runner := request.Options.Runner
	if request.Mode != runspec.IntakeExecute || runner.Set && runner.Value.Kind != runspec.RunnerAny || slices.ContainsFunc(request.Options.Requires.Value, requiresPool) {
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
