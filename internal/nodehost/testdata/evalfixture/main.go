package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type pipeline struct{ sparkwing.Base }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ struct{}, _ sparkwing.RunContext) error {
	body := func(context.Context) error { fmt.Println("body ran"); return nil }
	source := sparkwing.Job(plan, "source", body)
	source.Generate(func(context.Context, *sparkwing.Plan) error { return errors.New("generator refused") })
	sparkwing.Job(plan, "memo", body).Memo(func(context.Context) (sparkwing.MemoValue, error) {
		return sparkwing.NoMemo, errors.New("memo refused")
	})
	sparkwing.Job(plan, "skip", body).SkipIf(func(context.Context) bool {
		panic("predicate refused")
	})
	return nil
}

func main() {
	sparkwing.Register[struct{}]("eval", func() sparkwing.Pipeline[struct{}] { return pipeline{} })
	sparkwing.Main()
}
