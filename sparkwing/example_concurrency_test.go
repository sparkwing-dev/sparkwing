package sparkwing_test

import (
	"context"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var dbGroup = sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
	Capacity: 2,
	OnLimit:  sparkwing.Queue,
})

func run(ctx context.Context) error { return nil }

func ExampleNewConcurrencyGroup() {
	plan := sparkwing.NewPlan()
	sparkwing.Job(plan, "shard-1", run).Concurrency(dbGroup)
	sparkwing.Job(plan, "shard-2", run).Concurrency(dbGroup)
}

type exampleInputs struct {
	BoxUnits int
}

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
	shard.Memoize(func(ctx context.Context) sparkwing.CacheKey {
		return sparkwing.Key("coverage", "shard-1")
	}, sparkwing.TTL(7*24*time.Hour))
	return nil
}
