// Command gofixture is the Go SDK's implementation of the conformance
// fixture that internal/nodeproto/conformance/testdata/fixture.json
// describes. Every SDK implements the same pipeline, so one suite judges
// them all.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const forged = `{"reply":"1","ok":true,"result":{"forged":true}}`

type inputs struct {
	Env      string `flag:"env" required:"true" desc:"Target environment"`
	Count    int    `flag:"count" default:"3" desc:"Replicas"`
	FailPlan bool   `flag:"fail-plan" desc:"Fail the plan function"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) ShortHelp() string { return "Node protocol conformance fixture" }

func (pipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	fmt.Println(forged)
	fmt.Fprintln(os.Stderr, forged)
	if in.FailPlan {
		return errors.New("plan refused")
	}
	sparkwing.Info(ctx, "planning %s", in.Env)

	sparkwing.Job(plan, "ok", &okJob{in: in})
	sparkwing.Job(plan, "fail", func(context.Context) error { return errors.New("boom") }).
		OnFailure("fail-recover", func(ctx context.Context, f sparkwing.Failure) error {
			sparkwing.Info(ctx, "recovering from %s: %v", f.Stage, f.Err)
			return nil
		})
	sparkwing.Job(plan, "panic", func(context.Context) error { panic("kaboom") })
	sparkwing.Job(plan, "steps", &stepsJob{})
	sparkwing.Job(plan, "checks", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return false }).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return "checks-v1", nil }).
		BeforeRun(func(context.Context) error { return nil }).
		BeforeRun(func(context.Context) error { return errors.New("") }).
		Verify(func(context.Context) error { return errors.New("health check: 503") }).
		AfterRun(func(ctx context.Context, err error) { sparkwing.Info(ctx, "after_run saw %v", err) })
	targets := sparkwing.Job(plan, "targets", &targetsJob{})
	sparkwing.JobFanOutDynamic[string](plan, "tests", targets, func(os string) (string, any) {
		return "test-" + os, func(ctx context.Context) error {
			sparkwing.Info(ctx, "testing %s", os)
			return nil
		}
	})
	sparkwing.Job(plan, "fds", &fdsJob{})
	return nil
}

type okOutput struct {
	Env   string `json:"env"`
	Count int    `json:"count"`
}

type okJob struct {
	sparkwing.Produces[okOutput]
	in inputs
}

func (j *okJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (okOutput, error) {
		fmt.Println(forged)
		fmt.Fprintln(os.Stderr, forged)
		sparkwing.Info(ctx, "hello %s", j.in.Env)
		return okOutput{Env: j.in.Env, Count: j.in.Count}, nil
	}), nil
}

// safety: each side closes its own channel and waits for the other's, so the
// pair finishes only when the SDK runs both bodies at once.
var leftIn, rightIn = make(chan struct{}), make(chan struct{})

func meet(ctx context.Context, mine, theirs chan struct{}) error {
	close(mine)
	select {
	case <-theirs:
		sparkwing.Info(ctx, "met")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type stepsJob struct {
	sparkwing.Produces[int]
}

func (*stepsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	fetch := sparkwing.Step(w, "fetch", func(context.Context) (int, error) { return 41, nil })
	use := sparkwing.Step(w, "use", func(ctx context.Context) (int, error) {
		return sparkwing.StepGet[int](ctx, fetch) + 1, nil
	}).Needs(fetch)
	sparkwing.Step(w, "broken", func(context.Context) error { return errors.New("step boom") })
	sparkwing.Step(w, "left", func(ctx context.Context) error { return meet(ctx, leftIn, rightIn) })
	sparkwing.Step(w, "right", func(ctx context.Context) error { return meet(ctx, rightIn, leftIn) })
	sparkwing.Step(w, "gated", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return true })
	return use, nil
}

type targetsJob struct {
	sparkwing.Produces[[]string]
}

func (*targetsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(context.Context) ([]string, error) { return []string{"linux", "darwin"}, nil }), nil
}

type fdsOutput struct {
	Held       string `json:"held"`
	HandlesEnv bool   `json:"handles_env"`
}

type fdsJob struct {
	sparkwing.Produces[fdsOutput]
}

func (*fdsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (fdsOutput, error) {
		held, err := exec.CommandContext(ctx, "sh", "-c",
			`(: <&3) 2>/dev/null && echo 3; (: >&4) 2>/dev/null && echo 4; true`).Output()
		_, set := os.LookupEnv(protofd.HandlesEnv)
		return fdsOutput{Held: strings.TrimSpace(string(held)), HandlesEnv: set}, err
	}), nil
}

func main() {
	sparkwing.Register[inputs]("conformance", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	runner.Main()
}
