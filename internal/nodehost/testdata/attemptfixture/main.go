// Command attemptfixture is a pipeline the attempt executor's tests run end
// to end: a node that reads a secret, prints it, registers a cleanup and
// returns an output; a node that exits mid-body; a plan that drifts
// between processes; and nodes with every attempt closure.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
)

type inputs struct {
	Marker  string `flag:"marker" desc:"Directory the cleanups touch files in"`
	Counter string `flag:"counter" desc:"File counting plans; set, each plan differs"`
	Crash   bool   `flag:"crash" desc:"Add a node that exits mid-body"`
	Leak    bool   `flag:"leak" desc:"Add a node whose error quotes the secret"`
	Hooks   bool   `flag:"hooks" desc:"Add nodes with skip_if, before_run, verify and after_run closures"`
}

type fixtureSecrets struct {
	Token string `sw:"DEPLOY_TOKEN,required"`
}

type pipeline struct{ sparkwing.Base }

func (pipeline) Secrets() any { return &fixtureSecrets{} }

func (pipeline) Plan(_ context.Context, plan *sparkwing.Plan, in inputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &buildJob{in: in}).Env("FIXTURE_ENV", "from-plan")
	if in.Counter != "" {
		data, _ := os.ReadFile(in.Counter)
		n, _ := strconv.Atoi(string(data))
		n++
		if err := os.WriteFile(in.Counter, []byte(strconv.Itoa(n)), 0o600); err != nil {
			return err
		}
		build.Env("PLAN_SEQ", strconv.Itoa(n))
	}
	if in.Crash {
		sparkwing.Job(plan, "crash", func(ctx context.Context) error {
			if _, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"touch", in.Marker + "/crash"}}); err != nil {
				return err
			}
			os.Exit(3)
			return nil
		})
	}
	if in.Leak {
		sparkwing.Job(plan, "leak", func(ctx context.Context) error {
			token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
			if err != nil {
				return err
			}
			return fmt.Errorf("deploy refused token %s", token)
		})
	}
	if in.Hooks {
		addHooks(plan, in.Marker)
	}
	return nil
}

// addHooks adds nodes that write each closure's call to <marker>/<node>, one
// line per call.
func addHooks(plan *sparkwing.Plan, marker string) {
	trace := func(node, line string) {
		f, err := os.OpenFile(filepath.Join(marker, node), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = fmt.Fprintln(f, line)
			_ = f.Close()
		}
	}
	after := func(node string) sparkwing.AfterRunFn {
		return func(_ context.Context, err error) { trace(node, fmt.Sprintf("after:%v", err)) }
	}
	sparkwing.Job(plan, "skipped", func(context.Context) error { trace("skipped", "body"); return nil }).
		SkipIf(func(context.Context) bool { return false }).
		SkipIf(func(context.Context) bool { return true })
	sparkwing.Job(plan, "kept", func(context.Context) error { trace("kept", "body"); return nil }).
		SkipIf(func(context.Context) bool { panic("no answer") }).
		SkipIf(func(ctx context.Context) bool { <-ctx.Done(); return true }, sparkwing.SkipBudget(300*time.Millisecond))
	sparkwing.Job(plan, "hooked", func(context.Context) error {
		data, _ := os.ReadFile(filepath.Join(marker, "hooked"))
		trace("hooked", "body")
		if !strings.Contains(string(data), "body") {
			return errors.New("first try fails")
		}
		return nil
	}).
		BeforeRun(func(context.Context) error { trace("hooked", "before"); return nil }).
		Verify(func(context.Context) error { trace("hooked", "verify"); return nil }).
		AfterRun(after("hooked"))
	sparkwing.Job(plan, "unverified", func(context.Context) error { trace("unverified", "body"); return nil }).
		Verify(func(context.Context) error { return errors.New("health check: 503") }).
		AfterRun(after("unverified"))
	sparkwing.Job(plan, "unready", func(context.Context) error { trace("unready", "body"); return nil }).
		BeforeRun(func(context.Context) error { return errors.New("not ready") }).
		AfterRun(after("unready"))
}

type buildOutput struct {
	Env  string `json:"env"`
	Run  string `json:"run"`
	Leak string `json:"leak"`
	AWS  string `json:"aws"`
}

type buildJob struct {
	sparkwing.Produces[buildOutput]
	in inputs
}

func (j *buildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", func(ctx context.Context) (buildOutput, error) {
		token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
		if err != nil {
			return buildOutput{}, err
		}
		fmt.Println("stdout token=" + token)
		sparkwing.Info(ctx, "record token=%s", token)
		if j.in.Marker != "" {
			if _, err := cleanup.Register(ctx, cleanup.Spec{Argv: []string{"sh", "-c", "echo \"$AWS_SECRET_ACCESS_KEY\" > " + j.in.Marker + "/build; echo cleaned " + token}}); err != nil {
				return buildOutput{}, err
			}
		}
		return buildOutput{Env: os.Getenv("FIXTURE_ENV"), Run: os.Getenv("SPARKWING_RUN_ID"), Leak: os.Getenv("SPARKWING_LEAK"), AWS: os.Getenv("AWS_SECRET_ACCESS_KEY")}, nil
	}), nil
}

func main() {
	sparkwing.Register[inputs]("attempts", func() sparkwing.Pipeline[inputs] { return pipeline{} })
	runner.Main()
}
