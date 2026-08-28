package jobs

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenVerifyTeardown struct{ sw.Base }

func (p GenVerifyTeardown) ShortHelp() string {
	return "fixture readiness + teardown generated pipeline"
}

func (p GenVerifyTeardown) Help() string { return p.ShortHelp() }

func (GenVerifyTeardown) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run verify-teardown"},
	}
}

func (GenVerifyTeardown) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	fixture := sw.Job(plan, "fixture", genFixtureStart).
		Verify(genFixtureReady).
		OnFailure("teardown-on-fixture-fail", func(ctx context.Context, _ sw.Failure) error {
			return genFixtureTeardown(ctx)
		})

	sw.Job(plan, "test", genFixtureSuite).
		Needs(fixture).
		Timeout(20 * time.Minute).
		AfterRun(func(ctx context.Context, _ error) { _ = genFixtureTeardown(ctx) })

	return nil
}

func genFixtureStart(ctx context.Context) error {
	sw.Info(ctx, "starting database container")
	return nil
}

func genFixtureReady(ctx context.Context) error {
	sw.Annotate(ctx, "database accepting connections")
	return nil
}

func genFixtureSuite(ctx context.Context) error {
	sw.Info(ctx, "running integration suite")
	return nil
}

func genFixtureTeardown(ctx context.Context) error {
	sw.Info(ctx, "removing database container")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("verify-teardown", func() sw.Pipeline[sw.NoInputs] { return &GenVerifyTeardown{} })
}
