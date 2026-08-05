package jobs

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
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
		Cache(genCachedKey, sw.TTL(24*time.Hour))
	return nil
}

// genCachedKey computes the content key after upstream dependencies
// complete. It runs on the runner, not while the DAG is built, so
// hashing the tree here is fine.
func genCachedKey(ctx context.Context) sw.CacheKey {
	sources, err := sw.Glob("**/*.go")
	if err != nil {
		return sw.NoCache
	}
	return sw.Key("test-suite", sources)
}

func genCachedTest(ctx context.Context) error {
	sw.Info(ctx, "running go test ./...")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("cached-suite", func() sw.Pipeline[sw.NoInputs] { return &GenCachedSuite{} })
}
