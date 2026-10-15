package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type pipeline struct{ sparkwing.Base }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ struct{}, _ sparkwing.RunContext) error {
	source := sparkwing.Job(plan, "source", func(context.Context) error { return nil })
	source.Generate(func(context.Context, *sparkwing.Plan) error { return errors.New("generator refused") })
	sparkwing.Job(plan, "memo", func(context.Context) error { return nil }).Memo(func(context.Context) (sparkwing.MemoValue, error) {
		return sparkwing.NoMemo, errors.New("memo refused")
	})
	sparkwing.Job(plan, "skip", func(context.Context) error { return nil }).SkipIf(func(context.Context) bool {
		panic("predicate refused")
	})
	return nil
}

func main() {
	sparkwing.Register[struct{}]("eval", func() sparkwing.Pipeline[struct{}] { return pipeline{} })
	sparkwing.Main()
}
