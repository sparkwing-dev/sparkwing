package jobs

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
)

type GenCachedSuite struct{ sw.Base }

func (p GenCachedSuite) ShortHelp() string { return "content-cached test suite generated pipeline" }

func (p GenCachedSuite) Help() string { return p.ShortHelp() }

func (GenCachedSuite) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run cached-suite"},
	}
}

func (GenCachedSuite) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	sw.Job(plan, "test", genCachedTest).
		Memoize(inputs.Files("**/*.go"), sw.TTL(24*time.Hour))
	return nil
}

func genCachedTest(ctx context.Context) error {
	sw.Info(ctx, "running go test ./...")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("cached-suite", func() sw.Pipeline[sw.NoInputs] { return &GenCachedSuite{} })
}
