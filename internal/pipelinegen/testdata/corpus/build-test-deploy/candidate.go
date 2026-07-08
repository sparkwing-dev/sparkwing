package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenBuildTestDeploy struct{ sw.Base }

func (p GenBuildTestDeploy) ShortHelp() string { return "build/test/deploy generated pipeline" }

func (p GenBuildTestDeploy) Help() string { return p.ShortHelp() }

func (GenBuildTestDeploy) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run gen-build-test-deploy"},
	}
}

func (GenBuildTestDeploy) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &genBTDBuild{})
	test := sw.Job(plan, "test", &genBTDTest{}).Needs(build)
	sw.Job(plan, "deploy", &genBTDDeploy{}).Needs(test)
	return nil
}

type genBTDBuild struct{ sw.Base }

func (j *genBTDBuild) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, `echo "build"`).Run()
		return err
	})
	return nil, nil
}

type genBTDTest struct{ sw.Base }

func (j *genBTDTest) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, `echo "test"`).Run()
		return err
	})
	return nil, nil
}

type genBTDDeploy struct{ sw.Base }

func (j *genBTDDeploy) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "run", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, `echo "deploy"`).Run()
		return err
	})
	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("build-test-deploy", func() sw.Pipeline[sw.NoInputs] { return &GenBuildTestDeploy{} })
}
