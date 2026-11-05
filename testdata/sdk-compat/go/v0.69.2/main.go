package main

import (
	"context"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type inputs struct {
	Name string `flag:"name" default:"guest"`
}

type output struct {
	Name string `json:"name"`
}

type pipeline struct{ sparkwing.Base }

type task struct {
	sparkwing.Produces[output]
	name string
}

func (task task) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	result := sparkwing.Step(work, "output", func(context.Context) (output, error) {
		return output{Name: task.name}, nil
	})
	for _, mode := range []string{"exec", "fail", "cancel"} {
		sparkwing.Step(work, mode, func(ctx context.Context) error {
			_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), mode).Run()
			return err
		})
	}
	sparkwing.Step(work, "environment", func(ctx context.Context) error {
		if err := os.Setenv("COMPAT_MUTATED_ENV", "callback"); err != nil {
			return err
		}
		_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "env").Run()
		return err
	})
	sparkwing.Step(work, "helper", func(ctx context.Context) (map[string]string, error) {
		value, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "trim").String()
		return map[string]string{"trimmed": value}, err
	})
	sparkwing.Step(work, "raw", func(ctx context.Context) error {
		if _, err := os.Stdout.WriteString("compat-raw-out\n"); err != nil {
			return err
		}
		_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "raw").Run()
		return err
	})
	return result, nil
}

func (pipeline) ShortHelp() string { return "Frozen historical SDK compatibility fixture" }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	source := sparkwing.Job(plan, "task", task{name: in.Name}).Retry(2).Timeout(time.Minute)
	source.Memo(func(context.Context) (sparkwing.MemoValue, error) { return sparkwing.MemoValue("compat-memo"), nil })
	source.Generate(func(_ context.Context, generated *sparkwing.Plan) error {
		sparkwing.Job(generated, "generated", func(context.Context) error { return nil })
		return nil
	})
	sparkwing.Job(plan, "dependent", func(context.Context) error { return nil }).Needs(source)
	return nil
}

func main() {
	sparkwing.Register[inputs]("compat", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
