package sparkwing_test

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type demoHello struct{ sw.Base }

func (demoHello) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
	sw.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sw.Info(ctx, "hello, sparkwing")
		return nil
	})
	return nil
}

func ExampleRegister() {
	sw.Register("hello", func() sw.Pipeline[sw.NoInputs] { return &demoHello{} })
}

type demoBuildOut struct {
	ImageTag string
	Digest   string
}

type demoBuild struct {
	sw.Base
	sw.Produces[demoBuildOut]
}

func (demoBuild) Work(w *sw.Work) (*sw.WorkStep, error) {
	compile := sw.Step(w, "compile", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, "docker build -t app:dev .").Run()
		return err
	})
	publish := sw.Step(w, "publish", func(ctx context.Context) (demoBuildOut, error) {
		return demoBuildOut{ImageTag: "app:dev", Digest: "sha256:..."}, nil
	})
	publish.Needs(compile)
	return publish, nil
}

type demoDeploy struct {
	sw.Base
	Build sw.Ref[demoBuildOut]
}

func (j *demoDeploy) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "apply", func(ctx context.Context) error {
		out := j.Build.Get(ctx)
		sw.Info(ctx, "deploying %s", out.ImageTag)
		return nil
	}).Risk("destructive", "prod")
	return nil, nil
}

func ExampleRefTo() {
	plan := sw.NewPlan()
	b := sw.Job(plan, "build", &demoBuild{})
	d := sw.Job(plan, "deploy", &demoDeploy{Build: sw.RefTo[demoBuildOut](b)})
	d.Needs(b)
}

func ExampleWorkStep_Risk() {
	plan := sw.NewPlan()
	sw.Job(plan, "prune-artifacts", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, "rm -rf /var/cache/sparkwing/old").Run()
		return err
	})
	_ = plan
}

func ExampleExec() {
	plan := sw.NewPlan()
	sw.Job(plan, "push-image", func(ctx context.Context) error {
		tag := "app:" + "dev"
		_, err := sw.Exec(ctx, "docker", "push", tag).Run()
		return err
	})
	_ = plan
}

func ExampleJobApproval() {
	plan := sw.NewPlan()
	b := sw.Job(plan, "build", &demoBuild{})
	gate := sw.JobApproval(plan, "promote-to-prod", sw.ApprovalConfig{
		Message:  "Promote build to production?",
		Timeout:  2 * time.Hour,
		OnExpiry: sw.ApprovalDeny,
	})
	gate.Job().Needs(b)
	d := sw.Job(plan, "deploy", &demoDeploy{Build: sw.RefTo[demoBuildOut](b)})
	d.Needs(gate.Job())
}
