package jobs

import (
	"context"
	"os"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenRuntimeBranch struct{ sw.Base }

func (p GenRuntimeBranch) ShortHelp() string {
	return "pipeline that branches on env in Plan (anti-pattern)"
}

func (p GenRuntimeBranch) Help() string { return p.ShortHelp() }

func (GenRuntimeBranch) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-runtime-branch"},
	}
}

func (GenRuntimeBranch) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	// Anti-pattern: branching the DAG on the host environment makes
	// Plan() non-deterministic. Express this as a job-level SkipIf,
	// Requires label, or a pipeline guard instead.
	if os.Getenv("DEPLOY_ENV") == "prod" {
		sw.Job(plan, "deploy-prod", &genRBJob{})
	} else {
		sw.Job(plan, "deploy-staging", &genRBJob{})
	}
	return nil
}

type genRBJob struct{ sw.Base }

func (j *genRBJob) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error { return nil })
	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("runtime-branch", func() sw.Pipeline[sw.NoInputs] { return &GenRuntimeBranch{} })
}
