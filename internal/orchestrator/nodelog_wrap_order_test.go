package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const wrapOrderSecret = "wrap-order-supersecret"

type leakyAnnotateJob struct{ sparkwing.Base }

func (j *leakyAnnotateJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "publish", j.run)
	return nil, nil
}

func (leakyAnnotateJob) run(ctx context.Context) error {
	token, err := sparkwing.Secret(ctx, "WRAP_TOKEN")
	if err != nil {
		return err
	}
	sparkwing.Annotate(ctx, "deployed with token "+token)
	sparkwing.Summary(ctx, "## Result\n\ntoken "+token+" accepted")
	return nil
}

type wrapOrderPipe struct{ sparkwing.Base }

func (wrapOrderPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "publish", &leakyAnnotateJob{})
	return nil
}

func init() {
	register("wrap-order-leak", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &wrapOrderPipe{} })
}

func TestRun_AnnotationsAndSummariesPersistMasked(t *testing.T) {
	p := newPaths(t)
	dotenv := filepath.Join(t.TempDir(), "secrets.env")
	if err := secrets.WriteDotenvEntry(dotenv, "WRAP_TOKEN", wrapOrderSecret); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "wrap-order-leak",
		SecretSource: secrets.NewDotenvSource(dotenv),
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q, want success (err=%v)", res.Status, res.Error)
	}

	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	steps, err := st.ListNodeSteps(ctx, res.RunID)
	if err != nil {
		t.Fatalf("ListNodeSteps: %v", err)
	}
	var step *store.NodeStep
	for _, s := range steps {
		if s.NodeID == "publish" && s.StepID == "publish" {
			step = s
		}
	}
	if step == nil {
		t.Fatalf("step state row missing after the wrapper reorder: %+v", steps)
	}
	if step.Status != store.StepPassed {
		t.Fatalf("step status = %q, want passed", step.Status)
	}

	joined := strings.Join(step.Annotations, "\n")
	if strings.Contains(joined, wrapOrderSecret) {
		t.Fatalf("persisted annotation leaks the secret: %q", joined)
	}
	if !strings.Contains(joined, "deployed with token ***") {
		t.Fatalf("expected a redacted annotation, got %q", joined)
	}
	if strings.Contains(step.Summary, wrapOrderSecret) {
		t.Fatalf("persisted summary leaks the secret: %q", step.Summary)
	}
	if !strings.Contains(step.Summary, "token *** accepted") {
		t.Fatalf("expected a redacted summary, got %q", step.Summary)
	}

	node, err := st.GetNode(ctx, res.RunID, "publish")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if strings.Contains(strings.Join(node.Annotations, "\n")+node.Summary, wrapOrderSecret) {
		t.Fatalf("node row leaks the secret: %+v / %q", node.Annotations, node.Summary)
	}
}
