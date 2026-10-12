package orchestrator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type secretsReaderSec struct {
	Token string `sw:"DEPLOY_TOKEN,required"`
	Flag  string `sw:"NICE_TO_HAVE,optional"`
}

type secretsReaderPipe struct{ sparkwing.Base }

func (secretsReaderPipe) Secrets() any { return &secretsReaderSec{} }

var capturedSecrets *secretsReaderSec

func (secretsReaderPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "read", func(ctx context.Context) error {
		capturedSecrets = sparkwing.PipelineSecrets[secretsReaderSec](ctx)
		return nil
	})
	return nil
}

func init() {
	register("orch-sec-reader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &secretsReaderPipe{} })
}

type staticSource map[string]string

func (s staticSource) Read(name string) (string, bool, error) {
	v, ok := s[name]
	if !ok {
		return "", false, secrets.ErrSecretMissing
	}
	return v, true, nil
}

func TestOrchestratorRun_InstallsPipelineSecretsOnCtx(t *testing.T) {
	capturedSecrets = nil
	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "orch-sec-reader",
		SecretSource: staticSource{"DEPLOY_TOKEN": "swu_real"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "orch-sec-reader",
			Entrypoint: "SecReader",
		},
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if capturedSecrets == nil {
		t.Fatal("step body did not capture PipelineSecrets")
	}
	if capturedSecrets.Token != "swu_real" {
		t.Errorf("Token = %q", capturedSecrets.Token)
	}
	if capturedSecrets.Flag != "" {
		t.Errorf("Flag should be empty (optional missing): %q", capturedSecrets.Flag)
	}
}

func TestOrchestratorRun_MissingRequiredSecretFailsRun(t *testing.T) {
	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "orch-sec-reader",
		SecretSource: staticSource{},
	})
	if err != nil {
		t.Fatalf("RunLocal returned err: %v", err)
	}
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed (required secret missing)", res.Status)
	}
	if res.Error == nil || !errors.Is(res.Error, sparkwing.ErrSecretMissing) {
		t.Errorf("expected ErrSecretMissing in chain, got %v", res.Error)
	}
}

type preflightSec struct {
	Deploy string `sw:"DEPLOY_KEY"`
	Token  string `sw:"API_TOKEN,required"`
	Hook   string `sw:"SLACK_HOOK,optional"`
}

type preflightPipe struct{ sparkwing.Base }

func (preflightPipe) Secrets() any { return &preflightSec{} }

var preflightNodeRan bool

func (preflightPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "deploy", func(context.Context) error {
		preflightNodeRan = true
		return nil
	})
	return nil
}

func init() {
	register("orch-sec-preflight", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &preflightPipe{} })
}

// A laptop run checks every declared secret against its store before the
// first node, names each required one that is missing with the command that
// sets it, and lets a missing optional one pass.
func TestOrchestratorRun_SecretPreflightNamesEveryMissingSecretBeforeTheFirstNode(t *testing.T) {
	preflightNodeRan = false
	p := newPathsWithStore(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{Pipeline: "orch-sec-preflight"})
	if err != nil {
		t.Fatalf("RunLocal returned err: %v", err)
	}
	if res.Status != "failed" || res.Error == nil {
		t.Fatalf("status = %q (%v), want failed", res.Status, res.Error)
	}
	if preflightNodeRan {
		t.Fatal("a node ran although the preflight failed")
	}
	t.Logf("refusal:\n%s", res.Error)
	want := `pipeline "orch-sec-preflight" did not start: it declares 2 required secrets that this machine's local secret store does not hold.
- DEPLOY_KEY
  Fix: sparkwing secrets set --name DEPLOY_KEY --file <path>
- API_TOKEN
  Fix: sparkwing secrets set --name API_TOKEN --file <path>
If the run can do without a secret, declare it optional: tag its field in Secrets() ` + "`sw:\"NAME,optional\"`."
	if got := res.Error.Error(); got != want {
		t.Fatalf("refusal =\n%s\nwant\n%s", got, want)
	}
	if !errors.Is(res.Error, sparkwing.ErrSecretMissing) {
		t.Errorf("the refusal does not match ErrSecretMissing: %v", res.Error)
	}
}
