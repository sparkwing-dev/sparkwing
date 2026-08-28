package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenContradictoryGuards struct{ sw.Base }

func (p GenContradictoryGuards) ShortHelp() string {
	return "pipeline with unsatisfiable guards (anti-pattern)"
}

func (p GenContradictoryGuards) Help() string { return p.ShortHelp() }

func (GenContradictoryGuards) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run contradictory-guards"},
	}
}

func (GenContradictoryGuards) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", genGuardStep)
	sw.Job(plan, "deploy", genGuardStep).Needs(build)
	return nil
}

func genGuardStep(ctx context.Context) error {
	sw.Info(ctx, "running")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("contradictory-guards", func() sw.Pipeline[sw.NoInputs] {
		return &GenContradictoryGuards{}
	})
}
