package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenTypedHandoff struct{ sw.Base }

func (p GenTypedHandoff) ShortHelp() string { return "typed handoff generated pipeline" }

func (p GenTypedHandoff) Help() string { return p.ShortHelp() }

func (GenTypedHandoff) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run typed-handoff"},
	}
}

// handoffImage is the typed value the build job publishes and the
// deploy job consumes.
type handoffImage struct {
	Tag    string
	Digest string
}

func (GenTypedHandoff) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	build := sw.Job(plan, "build", &genHandoffBuild{})
	image := sw.RefTo[handoffImage](build)
	sw.Job(plan, "deploy", &genHandoffDeploy{Image: image}).Needs(build)
	return nil
}

type genHandoffBuild struct {
	sw.Base
	sw.Produces[handoffImage]
}

func (j *genHandoffBuild) Work(w *sw.Work) (*sw.WorkStep, error) {
	return sw.Step(w, "compile", func(ctx context.Context) (handoffImage, error) {
		out := handoffImage{Tag: "app:sha-abc1234", Digest: "sha256:0fa1c0ffee"}
		sw.Annotate(ctx, "built "+out.Tag)
		return out, nil
	}), nil
}

type genHandoffDeploy struct {
	sw.Base
	Image sw.Ref[handoffImage]
}

func (j *genHandoffDeploy) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "rollout", func(ctx context.Context) error {
		img := j.Image.Get(ctx)
		sw.Info(ctx, "rolling out %s (%s)", img.Tag, img.Digest)
		return nil
	})
	return nil, nil
}

func init() {
	sw.Register[sw.NoInputs]("typed-handoff", func() sw.Pipeline[sw.NoInputs] { return &GenTypedHandoff{} })
}
