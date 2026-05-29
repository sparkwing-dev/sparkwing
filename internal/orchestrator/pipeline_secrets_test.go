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

// secretsReader reads PipelineSecrets[T] from a step body.
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

// staticSource serves a small fixed map; missing names return
// ErrSecretMissing so optional fields stay empty without failing.
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
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "orch-sec-reader",
		SecretSource: staticSource{"DEPLOY_TOKEN": "swu_real"},
		PipelineYAML: &pipelines.Pipeline{
			Name:       "orch-sec-reader",
			Entrypoint: "SecReader",
			Secrets: pipelines.SecretsField{
				{Name: "DEPLOY_TOKEN", Required: true},
			},
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
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline:     "orch-sec-reader",
		SecretSource: staticSource{}, // no DEPLOY_TOKEN
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
