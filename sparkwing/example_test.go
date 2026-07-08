package sparkwing_test

import (
	"context"
	"time"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

// hello is the minimal pipeline shape: one job, one step. Embeds
// sw.Base so it satisfies the pipeline marker.
type demoHello struct{ sw.Base }

func (demoHello) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
	sw.Job(plan, rc.Pipeline, func(ctx context.Context) error {
		sw.Info(ctx, "hello, sparkwing")
		return nil
	})
	return nil
}

// ExampleRegister shows the canonical Plan-only registration: a
// pipeline struct with a Plan method, attached at init via
// [sw.Register]. `sparkwing run hello` invokes it.
func ExampleRegister() {
	sw.Register("hello", func() sw.Pipeline[sw.NoInputs] { return &demoHello{} })
}

// demoBuildOut is what the build job hands to the deploy job. Any JSON-
// serializable type works.
type demoBuildOut struct {
	ImageTag string
	Digest   string
}

// build embeds sw.Produces[demoBuildOut] so [sw.RefTo] can wire downstream
// consumers at Plan time with type checking.
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

// deploy reads the build's output through a typed Ref. The Ref field
// is wired at Plan time; .Get(ctx) resolves it inside the step body.
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

// ExampleRefTo wires a typed build → deploy data flow. [sw.RefTo]
// validates at Plan time that build embeds Produces[demoBuildOut]; the
// returned [sw.Ref] panics loudly at dispatch if the upstream hasn't
// completed, so missing Needs() edges crash the run rather than read
// stale state.
func ExampleRefTo() {
	plan := sw.NewPlan()
	b := sw.Job(plan, "build", &demoBuild{})
	d := sw.Job(plan, "deploy", &demoDeploy{Build: sw.RefTo[demoBuildOut](b)})
	d.Needs(b)
}

// ExampleWorkStep_Risk shows blast-radius marking. The Risk labels
// are author-defined; an operator must pass --sw-allow destructive
// (or list both labels) to run this step, or --sw-dry-run to preview
// without executing. Steps without Risk run freely.
func ExampleWorkStep_Risk() {
	plan := sw.NewPlan()
	sw.Job(plan, "prune-artifacts", func(ctx context.Context) error {
		_, err := sw.Bash(ctx, "rm -rf /var/cache/sparkwing/old").Run()
		return err
	})
	_ = plan
}

// ExampleExec shows the argv-style command builder. Prefer it over
// [sw.Bash] for invocations that don't need shell features: no shell
// parsing means no quoting concerns even when argv values contain
// spaces, dollar signs, or backticks.
func ExampleExec() {
	plan := sw.NewPlan()
	sw.Job(plan, "push-image", func(ctx context.Context) error {
		tag := "app:" + "dev"
		_, err := sw.Exec(ctx, "docker", "push", tag).Run()
		return err
	})
	_ = plan
}

// ExampleJobApproval inserts a human gate between build and deploy.
// The orchestrator pauses the run when it reaches the gate, surfaces
// the prompt in the dashboard, and proceeds only when an approver
// clicks through. If Timeout elapses with no answer, [sw.ApprovalDeny]
// fails the run; the alternative is sw.ApprovalApprove (auto-pass).
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
