package main

import (
	"context"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"github.com/sparkwing-dev/sparkwing/sparkwing/git"
)

type inputs struct {
	Name string `flag:"name" default:"guest"`
}

type output struct {
	Name string `json:"name"`
}

type pipeline struct{ sparkwing.Base }

type fixtureSecrets struct {
	Value string `sw:"COMPAT_SECRET,optional"`
}

func (pipeline) Secrets() any { return &fixtureSecrets{} }

type lifecycle struct{}

func (lifecycle) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	concurrent := func(ctx context.Context) error {
		_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "concurrent").Run()
		return err
	}
	a := sparkwing.Step(work, "concurrent-a", concurrent)
	b := sparkwing.Step(work, "concurrent-b", concurrent)
	sparkwing.Step(work, "retry", func(ctx context.Context) error {
		_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "retry").Run()
		return err
	}).Needs(a, b)
	return nil, nil
}

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
		if _, err := os.Stdout.WriteString("compat-raw-outer-first\ncompat-secret-"); err != nil {
			return err
		}
		if _, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "raw-outer-barrier").Capture(); err != nil {
			return err
		}
		if _, err := os.Stdout.WriteString("value\ncompat-raw-out\n"); err != nil {
			return err
		}
		_, err := sparkwing.Exec(ctx, os.Getenv("COMPAT_HELPER_EXE"), "raw").Run()
		return err
	})
	sparkwing.Step(work, "oidc", func(ctx context.Context) (map[string]string, error) {
		token, err := sparkwing.OIDCToken(ctx, "compat-audience")
		return map[string]string{"token": token}, err
	})
	sparkwing.Step(work, "secret", func(ctx context.Context) (map[string]string, error) {
		value, err := sparkwing.Secret(ctx, "COMPAT_SECRET")
		return map[string]string{"secret": value}, err
	})
	sparkwing.Step(work, "git", func(ctx context.Context) ([]string, error) {
		return git.ChangedFiles(ctx, sparkwing.WorkDir(), "")
	})
	sparkwing.Step(work, "docker", func(ctx context.Context) (docker.BuildResult, error) {
		return docker.Build(ctx, docker.BuildConfig{Image: "compat-image", Tags: []string{"fixture"}})
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
	sparkwing.Job(plan, "lifecycle", lifecycle{}).Retry(2).Timeout(time.Minute).
		Cache(sparkwing.Dir("compat-cache", sparkwing.KeyFromFile("compat-cache-key.txt")))
	sparkwing.Job(plan, "dependent", func(context.Context) error { return nil }).Needs(source)
	return nil
}

func main() {
	sparkwing.Register[inputs]("compat", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	sparkwing.Main()
}
