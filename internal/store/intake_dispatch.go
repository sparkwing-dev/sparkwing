package store

import "github.com/sparkwing-dev/sparkwing/internal/runspec"

// SelectDispatch is the one intake dispatch rule on every controller. Publish runs, local runs, burst runs, and a
// runner selected by name, here or GitHub Actions keep host dispatch. Every other executing run seals controller
// dispatch, including pool= requirements and a runner label such as class=cloud, so a pool or Cloud launcher, which
// never claims a whole-run trigger, can take its plan and work units.
func SelectDispatch(request runspec.Request) runspec.Dispatch {
	runner := request.Options.Runner
	if request.Mode != runspec.IntakeExecute || request.Origin == runspec.OriginLocal || request.Burst != nil || runner.Set && runner.Value.Kind != runspec.RunnerAny && runner.Value.Kind != runspec.RunnerLabel {
		return runspec.DispatchHost
	}
	return runspec.DispatchController
}
