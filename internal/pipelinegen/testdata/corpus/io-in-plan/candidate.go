package jobs

import (
	"context"
	"os"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenIOInPlan struct{ sw.Base }

func (p GenIOInPlan) ShortHelp() string { return "pipeline that reads a file in Plan (anti-pattern)" }

func (p GenIOInPlan) Help() string { return p.ShortHelp() }

func (GenIOInPlan) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-io-in-plan"},
	}
}

func (GenIOInPlan) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	// Anti-pattern: reading a file while the DAG is built. Plan() must
	// be pure-declarative; this should run inside a Job or Step body.
	data, _ := os.ReadFile("VERSION")
	_ = data
	sw.Job(plan, run.Pipeline, &genIOJob{})
	return nil
}

type genIOJob struct{ sw.Base }

func (j *genIOJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error { return nil })
	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("io-in-plan", func() sw.Pipeline[sw.NoInputs] { return &GenIOInPlan{} })
}
