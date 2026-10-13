// Command attemptfixture is a pipeline the attempt executor's tests run end
// to end: a node that reads a secret, prints it, registers a cleanup and
// returns an output; a node that exits mid-body; and a plan that drifts
// between processes.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/cleanup"
)

type inputs struct {
	Marker  string `flag:"marker" desc:"Directory the cleanups touch files in"`
	Counter string `flag:"counter" desc:"File counting plans; set, each plan differs"`
	Crash   bool   `flag:"crash" desc:"Add a node that exits mid-body"`
	Leak    bool   `flag:"leak" desc:"Add a node whose error quotes the secret"`
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
	return nil
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
