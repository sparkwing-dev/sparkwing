package orchestrator_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const excerptSecretValue = "excerpt-deploy-token"

// excerptBuildJob fails the way a real build step does: an ExecError
// whose message embeds the command's whole stderr, here long enough to
// need bounding and carrying a secret the step had resolved.
type excerptBuildJob struct{ sparkwing.Base }

func (j *excerptBuildJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "compile", j.run)
	return nil, nil
}

func (excerptBuildJob) run(ctx context.Context) error {
	token, err := sparkwing.Secret(ctx, "DEPLOY_TOKEN")
	if err != nil {
		return err
	}
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "pkg/thing/file_%03d.go:12: undefined: Helper\n", i)
	}
	fmt.Fprintf(&b, "auth token %s rejected\n", token)
	b.WriteString("FAIL\texit status 2")
	return &sparkwing.ExecError{Command: "go build ./...", Stderr: b.String(), ExitCode: 2}
}

type excerptNoopJob struct{ sparkwing.Base }

func (j *excerptNoopJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", func(context.Context) error { return nil })
	return nil, nil
}

type excerptPipe struct{ sparkwing.Base }

func (excerptPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	build := sparkwing.Job(plan, "build", &excerptBuildJob{})
	sparkwing.Job(plan, "deploy", &excerptNoopJob{}).Needs(build)
	return nil
}

func init() {
	register("excerpt-fail", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &excerptPipe{} })
}

// TestRun_FailedNodeRecordsBoundedMaskedExcerpt is the end-to-end shape
// of the change: what a failing node persists is a bounded, masked tail
// of the command output plus a pointer to the full log -- not the raw
// unbounded stderr. The cancelled downstream node is left alone: it has
// no output of its own, and going to the logs for it is exactly what
// this must not do.
func TestRun_FailedNodeRecordsBoundedMaskedExcerpt(t *testing.T) {
	p := newPaths(t)
	dotenv := t.TempDir() + "/secrets.env"
	if err := secrets.WriteDotenvEntry(dotenv, "DEPLOY_TOKEN", excerptSecretValue); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "excerpt-fail",
		SecretSource: secrets.NewDotenvSource(dotenv),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	build, err := st.GetNode(ctx, res.RunID, "build")
	if err != nil {
		t.Fatalf("GetNode build: %v", err)
	}
	if len(build.Error) > 8192 {
		t.Fatalf("persisted node error is %d bytes; the excerpt is not bounded", len(build.Error))
	}
	if !strings.Contains(build.Error, "FAIL\texit status 2") {
		t.Fatalf("persisted error dropped the conclusion of the output:\n%s", build.Error)
	}
	if strings.Contains(build.Error, "file_000.go") {
		t.Fatalf("persisted error kept the head of the output:\n%s", build.Error)
	}
	if strings.Contains(build.Error, excerptSecretValue) {
		t.Fatalf("persisted error leaked the secret value:\n%s", build.Error)
	}
	if !strings.Contains(build.Error, "auth token *** rejected") {
		t.Fatalf("persisted error missing the redacted line:\n%s", build.Error)
	}
	wantPointer := "sparkwing runs logs --run " + res.RunID + " --node build"
	if !strings.Contains(build.Error, wantPointer) {
		t.Fatalf("persisted error missing %q:\n%s", wantPointer, build.Error)
	}

	deploy, err := st.GetNode(ctx, res.RunID, "deploy")
	if err != nil {
		t.Fatalf("GetNode deploy: %v", err)
	}
	if deploy.Outcome != string(sparkwing.Cancelled) {
		t.Fatalf("deploy outcome = %q, want cancelled", deploy.Outcome)
	}
	if deploy.Error != "upstream-failed" {
		t.Fatalf("cancelled node error = %q, want the untouched cascade reason", deploy.Error)
	}
}
