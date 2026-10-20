package store

import "github.com/sparkwing-dev/sparkwing/internal/runspec"

// SelectDispatch is the one intake dispatch rule on every controller. Publish runs, local runs, burst runs, a runner
// selection other than any keep host dispatch. Every other executing run seals controller dispatch, including
// pool= requirements, so a pool launcher can take its plan and work units.
func SelectDispatch(request runspec.Request) runspec.Dispatch {
	runner := request.Options.Runner
	if request.Mode != runspec.IntakeExecute || request.Origin == runspec.OriginLocal || request.Burst != nil || runner.Set && runner.Value.Kind != runspec.RunnerAny {
		return runspec.DispatchHost
	}
	return runspec.DispatchController
}
