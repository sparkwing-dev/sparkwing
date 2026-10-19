package store

import (
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SelectDispatch is the one intake dispatch rule on every controller. Publish runs, local runs, burst runs, a runner
// selection other than any, and a pool= requirement keep host dispatch. Every other executing run seals controller
// dispatch, including name= and class= requirements, whose units skip ranking for the claimants they name.
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
