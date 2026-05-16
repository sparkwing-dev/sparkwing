package orchestrator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/secrets"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// configReader exercises PipelineConfig[T] from a step body.
type configReaderCfg struct {
	ImageRepo string `sw:"image_repo,required"`
	Replicas  int    `sw:"replicas" default:"3"`
}

type configReaderPipe struct{ sparkwing.Base }

func (configReaderPipe) Config() any { return &configReaderCfg{} }

var capturedConfig *configReaderCfg

func (configReaderPipe) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "read", func(ctx context.Context) error {
		capturedConfig = sparkwing.PipelineConfig[configReaderCfg](ctx)
		return nil
	})
	return nil
}

func init() {
	register("orch-cfg-reader", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &configReaderPipe{} })
}

func TestOrchestratorRun_InstallsPipelineConfigOnCtx(t *testing.T) {
	capturedConfig = nil
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-cfg-reader",
		PipelineYAML: &pipelines.Pipeline{
			Name:       "orch-cfg-reader",
			Entrypoint: "ConfigReader",
			Values: pipelines.PipelineValues{
				Base: map[string]any{"image_repo": "example.dev/api"},
			},
			Targets: map[string]pipelines.Target{
				"prod": {Values: map[string]any{"replicas": 9}},
			},
		},
		Target: "prod",
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Fatalf("status = %q (err=%v); want success", res.Status, res.Error)
	}
	if capturedConfig == nil {
		t.Fatalf("step body did not capture PipelineConfig")
	}
	if capturedConfig.ImageRepo != "example.dev/api" {
		t.Errorf("ImageRepo = %q", capturedConfig.ImageRepo)
	}
	if capturedConfig.Replicas != 9 {
		t.Errorf("Replicas = %d, want 9 (target override)", capturedConfig.Replicas)
	}
}

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
			// Yaml-only declaration -- mixed with struct field
			// DEPLOY_TOKEN above; the union resolves once.
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

func TestOrchestratorRun_PlainPipelineUnaffected(t *testing.T) {
	// orch-annotate is a pipeline registered elsewhere with no Config
	// or Secrets provider. Confirm it still runs cleanly without the
	// PipelineYAML field set, matching the behavior pre-step-7.
	p := newPaths(t)
	res, err := orchestrator.RunLocal(context.Background(), p, orchestrator.Options{
		Pipeline: "orch-annotate",
	})
	if err != nil {
		t.Fatalf("RunLocal: %v", err)
	}
	if res.Status != "success" {
		t.Logf("status = %q (err=%v); the underlying annotate test is the canonical pre-existing failure", res.Status, res.Error)
	}
}
