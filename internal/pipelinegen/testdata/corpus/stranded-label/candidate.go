package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenStrandedLabel struct{ sw.Base }

func (p GenStrandedLabel) ShortHelp() string {
	return "pipeline with unhonored runner labels (anti-pattern)"
}

func (p GenStrandedLabel) Help() string { return p.ShortHelp() }

func (GenStrandedLabel) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run stranded-label"},
	}
}

func (GenStrandedLabel) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	// Anti-pattern: a blank label matches no runner, so this job never
	// dispatches.
	sw.Job(plan, "build", genStrandedStep).Requires("")

	// Anti-pattern: Inline runs in-process on the dispatcher, so the
	// runner label can never be honored.
	sw.Job(plan, "setup", genStrandedStep).Inline().Requires("linux")

	return nil
}

func genStrandedStep(ctx context.Context) error {
	sw.Info(ctx, "running")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("stranded-label", func() sw.Pipeline[sw.NoInputs] { return &GenStrandedLabel{} })
}
