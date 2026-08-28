package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenTypedArgs struct{ sw.Base }

type GenTypedArgsInput struct {
	Environment string `flag:"environment" desc:"Target environment to deploy to (e.g. staging, prod)."`
	DryRun      bool   `flag:"dry-run" desc:"Render the manifests without applying them."`
}

func (p GenTypedArgs) ShortHelp() string { return "typed-args deploy generated pipeline" }

func (p GenTypedArgs) Help() string { return p.ShortHelp() }

func (GenTypedArgs) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Deploy to staging", Command: "sparkwing run typed-args --environment=staging"},
		{Comment: "Render without applying", Command: "sparkwing run typed-args --environment=prod --dry-run"},
	}
}

func (GenTypedArgs) Plan(ctx context.Context, plan *sw.Plan, in GenTypedArgsInput, run sw.RunContext) error {
	render := sw.Job(plan, "render", genArgsRender(in))
	sw.Job(plan, "apply", genArgsApply(in)).
		Needs(render).
		SkipIf(func(ctx context.Context) bool { return in.DryRun })
	return nil
}

func genArgsRender(in GenTypedArgsInput) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sw.Info(ctx, "rendering manifests for %s", in.Environment)
		return nil
	}
}

func genArgsApply(in GenTypedArgsInput) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sw.Annotate(ctx, "applied to "+in.Environment)
		return nil
	}
}

func init() {
	sw.Register[GenTypedArgsInput]("typed-args", func() sw.Pipeline[GenTypedArgsInput] { return &GenTypedArgs{} })
}
