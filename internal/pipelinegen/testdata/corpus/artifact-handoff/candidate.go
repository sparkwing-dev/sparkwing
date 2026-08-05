package jobs

import (
	"context"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type GenArtifactHandoff struct{ sw.Base }

func (p GenArtifactHandoff) ShortHelp() string { return "artifact handoff generated pipeline" }

func (p GenArtifactHandoff) Help() string { return p.ShortHelp() }

func (GenArtifactHandoff) Examples() []sw.Example {
	return []sw.Example{
		{Comment: "Run locally", Command: "sparkwing run artifact-handoff"},
	}
}

func (GenArtifactHandoff) Plan(ctx context.Context, plan *sw.Plan, _ sw.NoInputs, run sw.RunContext) error {
	compile := sw.Job(plan, "compile", genArtifactCompile).
		Outputs("dist/**")

	// Consumes stages compile's artifacts into this node's workspace and
	// implies Needs(compile), so the files exist wherever publish lands.
	sw.Job(plan, "publish", genArtifactPublish).
		Consumes(compile)

	return nil
}

func genArtifactCompile(ctx context.Context) error {
	sw.Info(ctx, "compiling binaries into dist/")
	return nil
}

func genArtifactPublish(ctx context.Context) error {
	sw.Info(ctx, "uploading staged dist/ artifacts")
	return nil
}

func init() {
	sw.Register[sw.NoInputs]("artifact-handoff", func() sw.Pipeline[sw.NoInputs] { return &GenArtifactHandoff{} })
}
