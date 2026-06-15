package sparkwing_test

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// dbGroup is defined once, above the pipeline, and shared by every
// member node. This mirrors the "count limit" example in the
// cache/concurrency split proposal.
var dbGroup = sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
	Capacity: 2,
	OnLimit:  sparkwing.Queue,
})

func run(ctx context.Context) error { return nil }

// ExampleNewConcurrencyGroup shows a count limit: at most two members
// of the "db" group run at once; the rest queue.
func ExampleNewConcurrencyGroup() {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "shard-1", run).Concurrency(dbGroup)
	sparkwing.Job(plan, "shard-2", run).Concurrency(dbGroup)
}

// exampleInputs stands in for a pipeline's typed Inputs; BoxUnits is an
// author-supplied per-machine budget.
type exampleInputs struct {
	BoxUnits int
}

// DBShards demonstrates budgeted admission plus independent caching:
// the concurrency group is built inside Plan() from a per-box arg, and
// Cache keys purely on content with no scope and no collision.
type DBShards struct {
	sparkwing.Base
}

func (DBShards) Plan(ctx context.Context, plan *sparkwing.Plan, in exampleInputs, rc sparkwing.RunContext) error {
	dbGroup := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
		Capacity: in.BoxUnits,
		OnLimit:  sparkwing.Queue,
	})

	shard := sparkwing.Job(plan, "shard-1", run)
	shard.Concurrency(dbGroup, 4)
	shard.Cache(func(ctx context.Context) sparkwing.CacheKey {
		return sparkwing.Key("coverage", "shard-1")
	}, sparkwing.TTL(7*24*time.Hour))
	return nil
}
